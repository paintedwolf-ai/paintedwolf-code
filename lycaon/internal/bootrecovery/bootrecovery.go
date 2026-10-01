// Package bootrecovery runs subsystem-owner startup recovery.
//
// Two entry shapes register. A journal entry replays recorded intent; a
// reconcile entry compares two sources of truth that share no journal, such as
// durable rows against content-addressed bytes.
package bootrecovery

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kind is what an entry does when it runs.
type Kind string

const (
	// KindJournal replays or reverses intent recorded before an effect.
	KindJournal Kind = "journal"
	// KindReconcile compares two sources of truth with no journal spanning them.
	KindReconcile Kind = "reconcile"
)

// Phase is when an entry may run.
type Phase string

const (
	// PhaseBuild runs during composition, before the server accepts traffic.
	// PhaseBuild settles subsystem owners needed before the first request.
	PhaseBuild Phase = "build"
	// PhaseServe runs after wiring and before normal serving.
	PhaseServe Phase = "serve"
)

// Entry is one subsystem owner's startup recovery.
type Entry struct {
	// Name identifies the entry in reports and in After edges.
	Name string
	// Kind records whether this replays a journal or reconciles two stores.
	Kind Kind
	// Phase pins the entry to build-time or post-wiring execution.
	Phase Phase
	// After names entries that must complete first.
	After []string
	// Run settles the subsystem owner's recoverable state.
	Run func(context.Context) error
}

// Outcome is what one entry did.
type Outcome struct {
	Name     string
	Kind     Kind
	Phase    Phase
	Err      error
	Duration time.Duration
	// Blocked names degraded dependencies that kept this entry from running.
	Blocked []string
}

// Degraded reports whether the entry ran and failed.
func (o Outcome) Degraded() bool { return o.Err != nil }

// Skipped reports whether a dependency's failure kept the entry from running.
func (o Outcome) Skipped() bool { return len(o.Blocked) > 0 }

// Report is every outcome from one phase, in execution order.
type Report struct {
	Phase    Phase
	Outcomes []Outcome
}

// UnsettledError identifies recovery entries blocking normal serving.
type UnsettledError struct {
	Phase    Phase
	Outcomes []Outcome
}

func (e *UnsettledError) Error() string {
	if e == nil || len(e.Outcomes) == 0 {
		return "startup recovery unsettled"
	}
	names := make([]string, 0, len(e.Outcomes))
	for _, outcome := range e.Outcomes {
		name := strings.TrimSpace(outcome.Name)
		if name != "" {
			names = append(names, name)
		}
	}
	return fmt.Sprintf("startup recovery %s phase unsettled: %s", e.Phase, strings.Join(names, ", "))
}

// Degraded lists entries that ran and failed, plus entries a dependency blocked.
func (r Report) Degraded() []Outcome {
	var out []Outcome
	for _, o := range r.Outcomes {
		if o.Degraded() || o.Skipped() {
			out = append(out, o)
		}
	}
	return out
}

// Err blocks normal serving while an entry remains unsettled.
func (r Report) Err() error {
	degraded := r.Degraded()
	if len(degraded) == 0 {
		return nil
	}
	return &UnsettledError{Phase: r.Phase, Outcomes: degraded}
}

// Registry contains every startup recovery entry.
type Registry struct {
	mu      sync.Mutex
	entries map[string]Entry
}

// New builds an empty registry.
func New() *Registry {
	return &Registry{entries: make(map[string]Entry)}
}

// Register adds one entry. A malformed or duplicate registration is a wiring
// defect, so it is returned rather than logged.
func (r *Registry) Register(e Entry) error {
	if r == nil {
		return fmt.Errorf("bootrecovery: nil registry")
	}
	name := strings.TrimSpace(e.Name)
	if name == "" {
		return fmt.Errorf("bootrecovery: entry needs a name")
	}
	if e.Run == nil {
		return fmt.Errorf("bootrecovery: entry %q needs a Run function", name)
	}
	switch e.Kind {
	case KindJournal, KindReconcile:
	default:
		return fmt.Errorf("bootrecovery: entry %q has unknown kind %q", name, e.Kind)
	}
	switch e.Phase {
	case PhaseBuild, PhaseServe:
	default:
		return fmt.Errorf("bootrecovery: entry %q has unknown phase %q", name, e.Phase)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[name]; exists {
		return fmt.Errorf("bootrecovery: entry %q registered twice", name)
	}
	e.Name = name
	r.entries[name] = e
	return nil
}

// Run executes one phase in dependency order.
//
// An unknown After edge or a cycle returns an error: the registry itself is
// wrong. A failing recovery is recorded in the report so the caller can expose
// every unsettled subsystem owner while serving remains closed.
func (r *Registry) Run(ctx context.Context, phase Phase) (Report, error) {
	if r == nil {
		return Report{Phase: phase}, nil
	}
	r.mu.Lock()
	all := make(map[string]Entry, len(r.entries))
	for name, e := range r.entries {
		all[name] = e
	}
	r.mu.Unlock()

	order, err := resolveOrder(all, phase)
	if err != nil {
		return Report{Phase: phase}, err
	}

	report := Report{Phase: phase, Outcomes: make([]Outcome, 0, len(order))}
	failed := make(map[string]struct{})
	for _, name := range order {
		e := all[name]
		if blocked := blockedBy(e, all, failed); len(blocked) > 0 {
			report.Outcomes = append(report.Outcomes, Outcome{
				Name: e.Name, Kind: e.Kind, Phase: e.Phase, Blocked: blocked,
			})
			failed[name] = struct{}{}
			continue
		}
		started := time.Now()
		runErr := e.Run(ctx)
		outcome := Outcome{
			Name: e.Name, Kind: e.Kind, Phase: e.Phase,
			Err: runErr, Duration: time.Since(started),
		}
		if runErr != nil {
			failed[name] = struct{}{}
		}
		report.Outcomes = append(report.Outcomes, outcome)
	}
	return report, nil
}

// blockedBy lists this entry's degraded dependencies. A dependent does not run
// on state its dependency failed to settle.
func blockedBy(e Entry, all map[string]Entry, failed map[string]struct{}) []string {
	var out []string
	for _, dep := range e.After {
		dep = strings.TrimSpace(dep)
		if dep == "" {
			continue
		}
		if _, known := all[dep]; !known {
			continue
		}
		if _, bad := failed[dep]; bad {
			out = append(out, dep)
		}
	}
	sort.Strings(out)
	return out
}

// resolveOrder topologically sorts one phase's entries, breaking ties by name
// so the same registry always runs in the same order.
func resolveOrder(all map[string]Entry, phase Phase) ([]string, error) {
	inPhase := make(map[string]Entry)
	for name, e := range all {
		if e.Phase == phase {
			inPhase[name] = e
		}
	}
	for name, e := range inPhase {
		for _, dep := range e.After {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			target, known := all[dep]
			if !known {
				return nil, fmt.Errorf("bootrecovery: entry %q depends on unregistered %q", name, dep)
			}
			// A build-phase dependency is already settled when serve runs.
			if target.Phase == phase {
				continue
			}
			if target.Phase == PhaseServe && phase == PhaseBuild {
				return nil, fmt.Errorf("bootrecovery: build entry %q depends on serve entry %q", name, dep)
			}
		}
	}

	pending := make([]string, 0, len(inPhase))
	for name := range inPhase {
		pending = append(pending, name)
	}
	sort.Strings(pending)

	done := make(map[string]bool, len(pending))
	out := make([]string, 0, len(pending))
	for len(out) < len(pending) {
		progressed := false
		for _, name := range pending {
			if done[name] {
				continue
			}
			ready := true
			for _, dep := range inPhase[name].After {
				dep = strings.TrimSpace(dep)
				if dep == "" {
					continue
				}
				if _, sameName := inPhase[dep]; !sameName {
					continue
				}
				if !done[dep] {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			done[name] = true
			out = append(out, name)
			progressed = true
		}
		if !progressed {
			var stuck []string
			for _, name := range pending {
				if !done[name] {
					stuck = append(stuck, name)
				}
			}
			return nil, fmt.Errorf("bootrecovery: dependency cycle among %s", strings.Join(stuck, ", "))
		}
	}
	return out, nil
}
