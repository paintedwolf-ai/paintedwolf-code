package briefing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Source records how a section's text was produced.
type Source string

const (
	SourceSummarizer    Source = "summarizer"
	SourceFallback      Source = "fallback"
	SourceDeterministic Source = "deterministic"
)

// Result is a section's stored text for one manifest hash.
type Result struct {
	Text      string
	Source    Source
	Provider  string
	Model     string
	Quality   []string
	CreatedAt time.Time
}

// SectionMode states whether the model may be asked for a section.
type SectionMode int

const (
	// Generated sections are asked of the model when no result exists.
	Generated SectionMode = iota
	// Lookup sections use a stored result and are otherwise absent.
	Lookup
	// Deterministic sections carry text computed from host facts.
	Deterministic
)

// SectionPlan is one resolved section of a target, built with bounded work
// and no model call.
type SectionPlan struct {
	ID       string
	Mode     SectionMode
	Manifest Manifest
	// Prompt is required for Generated sections.
	Prompt Prompt
	// Text is the Deterministic section's value.
	Text string
	// Fallback is factual text used when generation yields nothing usable.
	Fallback string
}

// Prompt is the rendered utility request for a generated section.
type Prompt struct {
	System    string
	User      string
	MaxTokens int
}

// Request asks for one resolved target.
type Request struct {
	ProjectID  string
	ProjectDir string
	ProfileID  string
	Version    string
	TargetKey  string
	Manual     bool
	Policy     Policy
	Sections   []SectionPlan
}

// SectionState is a section of a target's current resolution.
type SectionState struct {
	ID           string
	ManifestHash string
	Result       *Result
	Reused       bool
}

// Status is a target's settlement.
type Status string

const (
	StatusPending  Status = "pending"
	StatusComplete Status = "complete"
	StatusFailed   Status = "failed"
)

// Snapshot is a target's state as one request or completion sees it.
type Snapshot struct {
	ProfileID string
	TargetKey string
	Status    Status
	AttemptID string
	Sections  []SectionState
	RecordID  string
	Error     string
	// Regenerate answers a manual request: whether any section needed work.
	Regenerate RegenerateOutcome
}

// RegenerateOutcome reports what a manual request did.
type RegenerateOutcome string

const (
	InputsUnchanged RegenerateOutcome = "inputs_unchanged"
	Started         RegenerateOutcome = "started"
)

// Results stores section text by manifest hash. Put inserts, or upgrades a
// fallback to a summarizer result; it never replaces summarizer text.
type Results interface {
	GetResults(ctx context.Context, projectID string, hashes []string) (map[string]Result, error)
	PutResult(ctx context.Context, projectID string, manifest Manifest, hash string, result Result) error
}

// Attempts owns target attempt state. Complete applies only for the active
// attempt, so a superseded attempt cannot publish over its successor.
type Attempts interface {
	StartAttempt(ctx context.Context, request Request, hashes []SectionHash) (attemptID string, joined bool, err error)
	CompleteAttempt(ctx context.Context, request Request, attemptID string, sections []SectionHash, recordID string) (applied bool, err error)
	FailAttempt(ctx context.Context, request Request, attemptID string, message string) (applied bool, err error)
}

// Records keeps the immutable record a completed target points at.
type Records interface {
	PutRecord(ctx context.Context, projectID, recordID, profileID, version string, sections []SectionHash) error
}

// Store is the persistence the engine needs.
type Store interface {
	Results
	Attempts
	Records
}

// Generator invokes the utility model for one section.
type Generator interface {
	Generate(ctx context.Context, invocation Invocation) (text string, provider, model string, err error)
}

// Invocation is one utility call. Requested binds the class for work a person
// is waiting on; other work binds the overlay class.
type Invocation struct {
	ProjectID  string
	ProjectDir string
	Purpose    string
	Requested  bool
	Prompt     Prompt
}

// Publisher announces committed snapshots.
type Publisher interface {
	PublishSnapshot(ctx context.Context, projectID string, snapshot Snapshot)
}

// Engine resolves targets against stored results and generates only missing
// sections, sharing in-flight work by manifest hash across targets.
type Engine struct {
	store     Store
	generator Generator
	publisher Publisher
	scheduler *Scheduler
	logger    *slog.Logger
	flights   flightGroup
}

func NewEngine(scheduler *Scheduler, store Store, generator Generator, publisher Publisher, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{store: store, generator: generator, publisher: publisher, scheduler: scheduler, logger: logger}
}

type resolved struct {
	request Request
	hashes  []SectionHash
	results map[string]Result
	missing []int
}

func (e *Engine) resolve(ctx context.Context, request Request) (resolved, error) {
	out := resolved{request: request, results: map[string]Result{}}
	var lookup []string
	for i, section := range request.Sections {
		if section.ID != section.Manifest.SectionID {
			return out, fmt.Errorf("briefing section %q carries manifest for %q", section.ID, section.Manifest.SectionID)
		}
		hash, err := section.Manifest.Hash()
		if err != nil {
			return out, err
		}
		out.hashes = append(out.hashes, SectionHash{SectionID: section.ID, ManifestHash: hash})
		switch section.Mode {
		case Deterministic:
			out.results[hash] = Result{Text: section.Text, Source: SourceDeterministic}
		case Generated:
			if section.Prompt.User == "" {
				return out, fmt.Errorf("briefing section %q has no prompt", section.ID)
			}
			lookup = append(lookup, hash)
			out.missing = append(out.missing, i)
		default:
			lookup = append(lookup, hash)
		}
	}
	stored, err := e.store.GetResults(ctx, request.ProjectID, lookup)
	if err != nil {
		return out, err
	}
	for hash, result := range stored {
		out.results[hash] = result
	}
	missing := out.missing[:0]
	for _, i := range out.missing {
		if result, ok := out.results[out.hashes[i].ManifestHash]; !ok || result.Source == SourceFallback {
			missing = append(missing, i)
		}
	}
	out.missing = missing
	return out, nil
}

// present lists the sections that have text, which is what a record holds.
func (r resolved) present() []SectionHash {
	var out []SectionHash
	for _, hash := range r.hashes {
		if _, ok := r.results[hash.ManifestHash]; ok {
			out = append(out, hash)
		}
	}
	return out
}

func (r resolved) snapshot(status Status, attempt, recordID string, before map[string]Result) Snapshot {
	snap := Snapshot{ProfileID: r.request.ProfileID, TargetKey: r.request.TargetKey, Status: status, AttemptID: attempt, RecordID: recordID}
	for _, hash := range r.hashes {
		state := SectionState{ID: hash.SectionID, ManifestHash: hash.ManifestHash}
		if result, ok := r.results[hash.ManifestHash]; ok {
			result := result
			state.Result = &result
			prior, existed := before[hash.ManifestHash]
			state.Reused = existed && result.Source != SourceDeterministic && prior.Source == result.Source && prior.Text == result.Text
		}
		snap.Sections = append(snap.Sections, state)
	}
	return snap
}

// Request returns a complete snapshot when every section is stored, lookup
// or deterministic, and otherwise admits or joins one attempt.
func (e *Engine) Request(ctx context.Context, request Request) (Snapshot, error) {
	r, err := e.resolve(ctx, request)
	if err != nil {
		return Snapshot{}, err
	}
	stored := map[string]Result{}
	for hash, result := range r.results {
		stored[hash] = result
	}
	if len(r.missing) == 0 {
		snap, err := e.settle(ctx, r, "", stored)
		if request.Manual {
			snap.Regenerate = InputsUnchanged
		}
		return snap, err
	}
	attempt, joined, err := e.store.StartAttempt(ctx, request, r.hashes)
	if err != nil {
		return Snapshot{}, err
	}
	snap := r.snapshot(StatusPending, attempt, "", stored)
	if request.Manual {
		snap.Regenerate = Started
	}
	if joined {
		// A manual request promotes queued work it joins.
		if request.Manual {
			e.scheduler.Promote(jobKey(request))
		}
		return snap, nil
	}
	job := Job{
		Key: jobKey(request), Project: request.ProjectID,
		Manual: request.Manual, Policy: request.Policy,
		Run:     func(ctx context.Context) { e.generate(ctx, r, attempt, stored) },
		Expired: func(ctx context.Context) { e.fail(ctx, request, attempt, ErrQueueExpired.Error()) },
	}
	if _, err := e.scheduler.Submit(job); err != nil {
		e.fail(context.WithoutCancel(ctx), request, attempt, err.Error())
		return Snapshot{}, err
	}
	return snap, nil
}

func (e *Engine) settle(ctx context.Context, r resolved, attempt string, stored map[string]Result) (Snapshot, error) {
	for _, hash := range r.hashes {
		result, ok := r.results[hash.ManifestHash]
		if !ok || result.Source != SourceDeterministic {
			continue
		}
		// Deterministic text is a function of its manifest, so storing it again is a no-op.
		if err := e.store.PutResult(ctx, r.request.ProjectID, r.manifest(hash.SectionID), hash.ManifestHash, result); err != nil {
			return Snapshot{}, err
		}
	}
	sections := r.present()
	recordID := RecordID(r.request.ProfileID, r.request.Version, sections)
	if err := e.store.PutRecord(ctx, r.request.ProjectID, recordID, r.request.ProfileID, r.request.Version, sections); err != nil {
		return Snapshot{}, err
	}
	if attempt != "" {
		applied, err := e.store.CompleteAttempt(ctx, r.request, attempt, sections, recordID)
		if err != nil || !applied {
			return Snapshot{}, errors.Join(err, errSuperseded)
		}
	}
	snap := r.snapshot(StatusComplete, attempt, recordID, stored)
	if e.publisher != nil && attempt != "" {
		e.publisher.PublishSnapshot(ctx, r.request.ProjectID, snap)
	}
	return snap, nil
}

var errSuperseded = errors.New("briefing attempt was superseded")

func jobKey(request Request) string {
	return request.ProjectID + "\x00" + request.ProfileID + "\x00" + request.TargetKey
}

func (r resolved) manifest(sectionID string) Manifest {
	for _, section := range r.request.Sections {
		if section.ID == sectionID {
			return section.Manifest
		}
	}
	return Manifest{}
}

func (e *Engine) generate(ctx context.Context, r resolved, attempt string, stored map[string]Result) {
	var failures []string
	for _, i := range r.missing {
		section := r.request.Sections[i]
		hash := r.hashes[i].ManifestHash
		result, err := e.flights.do(ctx, r.request.ProjectID+"\x00"+hash, func() (Result, error) {
			return e.generateSection(ctx, r.request, section, hash)
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures = append(failures, section.ID+": "+err.Error())
			continue
		}
		r.results[hash] = result
	}
	settleCtx := context.WithoutCancel(ctx)
	if len(failures) > 0 && len(r.present()) == 0 {
		e.fail(settleCtx, r.request, attempt, strings.Join(failures, "; "))
		return
	}
	if _, err := e.settle(settleCtx, r, attempt, stored); err != nil && !errors.Is(err, errSuperseded) {
		e.logger.WarnContext(settleCtx, "settle briefing", "profile", r.request.ProfileID, "target", r.request.TargetKey, "error", err)
	}
}

func (e *Engine) generateSection(ctx context.Context, request Request, section SectionPlan, hash string) (Result, error) {
	contract := section.Manifest.Contract
	if e.generator != nil {
		raw, provider, model, err := e.generator.Generate(ctx, Invocation{ProjectID: request.ProjectID, ProjectDir: request.ProjectDir, Purpose: request.ProfileID, Requested: request.Manual, Prompt: section.Prompt})
		if err == nil {
			if text, parseErr := contract.Parse(raw); parseErr == nil {
				result := Result{Text: text, Source: SourceSummarizer, Provider: provider, Model: model, CreatedAt: time.Now().UTC()}
				return result, e.store.PutResult(context.WithoutCancel(ctx), request.ProjectID, section.Manifest, hash, result)
			}
		} else if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
	}
	if section.Fallback == "" {
		return Result{}, ErrNoText
	}
	result := Result{Text: section.Fallback, Source: SourceFallback, CreatedAt: time.Now().UTC()}
	return result, e.store.PutResult(context.WithoutCancel(ctx), request.ProjectID, section.Manifest, hash, result)
}

func (e *Engine) fail(ctx context.Context, request Request, attempt, message string) {
	applied, err := e.store.FailAttempt(ctx, request, attempt, message)
	if err != nil {
		e.logger.WarnContext(ctx, "fail briefing", "profile", request.ProfileID, "target", request.TargetKey, "error", err)
		return
	}
	if applied && e.publisher != nil {
		e.publisher.PublishSnapshot(ctx, request.ProjectID, Snapshot{ProfileID: request.ProfileID, TargetKey: request.TargetKey, Status: StatusFailed, AttemptID: attempt, Error: message})
	}
}

// flightGroup shares one generation per manifest hash across targets.
type flightGroup struct {
	mu      sync.Mutex
	flights map[string]*flight
}

type flight struct {
	done   chan struct{}
	result Result
	err    error
}

func (g *flightGroup) do(ctx context.Context, key string, fn func() (Result, error)) (Result, error) {
	g.mu.Lock()
	if g.flights == nil {
		g.flights = map[string]*flight{}
	}
	if f, ok := g.flights[key]; ok {
		g.mu.Unlock()
		select {
		case <-f.done:
			return f.result, f.err
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	g.flights[key] = f
	g.mu.Unlock()
	f.result, f.err = fn()
	g.mu.Lock()
	delete(g.flights, key)
	g.mu.Unlock()
	close(f.done)
	return f.result, f.err
}
