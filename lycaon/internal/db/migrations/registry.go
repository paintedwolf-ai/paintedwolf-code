// Package migrations plans and executes explicitly registered released-schema upgrades.
package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Baseline identifies a schema independently of the product version.
type Baseline struct {
	Revision int    `json:"revision"`
	Shape    string `json:"shape"`
}

// Step is a reviewed database-only transformation. Checksum identifies its immutable source.
type Step struct {
	// Source embeds the actual implementation file or executed SQL definition.
	Source       []byte
	ScratchBytes uint64
	ID           string
	Checksum     string
	From         Baseline
	To           Baseline
	Apply        func(context.Context, *sql.Tx) error
	Validate     func(context.Context, *sql.Tx) error
}

// Plan contains a complete route; callers cannot replace its registered steps.
type Plan struct {
	Source       Baseline `json:"source"`
	Target       Baseline `json:"target"`
	steps        []Step
	scratchBytes uint64
}

// Required reports whether the store needs transformation.
func (p Plan) Required() bool { return len(p.steps) != 0 }

// ScratchBytes is the checked sum of declared migration working-space budgets.
func (p Plan) ScratchBytes() uint64 { return p.scratchBytes }

// Registry contains the current shape, immutable released shapes, and their upgrade routes.
type Registry struct {
	current Baseline
	known   map[int]Baseline
	steps   map[int]Step
	byID    map[string]Step
}

var ErrUnsupported = errors.New("unsupported store schema")

// New rejects ambiguous registrations and incomplete routes before any store is opened.
func New(current Baseline, released []Baseline, steps []Step) (*Registry, error) {
	r := &Registry{current: current, known: map[int]Baseline{}, steps: map[int]Step{}, byID: map[string]Step{}}
	for _, baseline := range append(append([]Baseline{}, released...), current) {
		if baseline.Revision < 1 || !validDigest(baseline.Shape) {
			return nil, fmt.Errorf("invalid schema baseline %d", baseline.Revision)
		}
		if existing, ok := r.known[baseline.Revision]; ok && existing != baseline {
			return nil, fmt.Errorf("schema revision %d has conflicting shapes", baseline.Revision)
		}
		r.known[baseline.Revision] = baseline
	}
	for _, step := range steps {
		if err := r.register(step); err != nil {
			return nil, err
		}
	}
	for _, baseline := range r.known {
		if _, err := r.Plan(baseline); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Registry) register(step Step) error {
	if strings.TrimSpace(step.ID) == "" || !validDigest(step.Checksum) || step.Apply == nil {
		return fmt.Errorf("invalid migration registration %q", step.ID)
	}
	sum := sha256.Sum256(step.Source)
	if len(step.Source) == 0 || hex.EncodeToString(sum[:]) != step.Checksum {
		return fmt.Errorf("migration %q source differs from its recorded checksum", step.ID)
	}
	step.Source = append([]byte(nil), step.Source...)
	if step.To.Revision != step.From.Revision+1 || r.known[step.From.Revision] != step.From || r.known[step.To.Revision] != step.To {
		return fmt.Errorf("migration %q does not connect adjacent known revisions", step.ID)
	}
	if _, exists := r.steps[step.From.Revision]; exists {
		return fmt.Errorf("duplicate migration from revision %d", step.From.Revision)
	}
	if _, exists := r.byID[step.ID]; exists {
		return fmt.Errorf("duplicate migration ID %q", step.ID)
	}
	r.steps[step.From.Revision], r.byID[step.ID] = step, step
	return nil
}

// Plan refuses unknown shapes and downgrades, including development shapes with a released marker.
func (r *Registry) Plan(source Baseline) (Plan, error) {
	p := Plan{Source: source, Target: r.current}
	if source.Revision > r.current.Revision || r.known[source.Revision] != source {
		return p, fmt.Errorf("%w: revision %d shape %s", ErrUnsupported, source.Revision, source.Shape)
	}
	for next := source; next != r.current; {
		step, ok := r.steps[next.Revision]
		if !ok {
			return p, fmt.Errorf("%w: no migration from revision %d", ErrUnsupported, next.Revision)
		}
		if step.ScratchBytes > ^uint64(0)-p.scratchBytes {
			return p, fmt.Errorf("migration scratch budget overflows from revision %d", source.Revision)
		}
		p.scratchBytes += step.ScratchBytes
		p.steps = append(p.steps, step)
		next = step.To
	}
	return p, nil
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}
