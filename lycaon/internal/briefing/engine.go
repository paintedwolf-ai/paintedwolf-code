package briefing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
)

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
	epoch   int64
	hashes  []SectionHash
	results map[string]Result
	missing []int
}

func (e *Engine) resolve(ctx context.Context, request Request) (resolved, error) {
	out := resolved{request: request, results: map[string]Result{}}
	epoch, err := e.store.Epoch(ctx, request.ProjectID)
	if err != nil {
		return out, err
	}
	out.epoch = epoch
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

func (r resolved) snapshot(status Status, attempt, recordID string, revision int64, before map[string]Result) Snapshot {
	snap := Snapshot{ProfileID: r.request.ProfileID, TargetKey: r.request.TargetKey, Status: status, AttemptID: attempt, RecordID: recordID, Epoch: r.epoch, Revision: revision}
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
	started, err := e.store.StartAttempt(ctx, request, r.epoch, r.hashes)
	if err != nil {
		return Snapshot{}, err
	}
	attempt := started.ID
	snap := r.snapshot(StatusPending, attempt, "", started.Revision, stored)
	if request.Manual {
		snap.Regenerate = Started
	}
	if started.Joined {
		// A manual request promotes queued work it joins.
		if request.Manual {
			e.scheduler.Promote(jobKey(request))
		}
		return snap, nil
	}
	job := Job{
		Key: jobKey(request), Project: request.ProjectID,
		Manual: request.Manual, Policy: request.Policy,
		Run: func(ctx context.Context) { e.generate(ctx, r, attempt, stored) },
		Expired: func(ctx context.Context) {
			e.fail(ctx, r, attempt, Failure{Code: CodeQueueExpired, Message: ErrQueueExpired.Error()})
		},
		Values: ctx,
	}
	if _, err := e.scheduler.Submit(job); err != nil {
		e.fail(context.WithoutCancel(ctx), r, attempt, Failure{Code: CodeAdmissionRefused, Message: err.Error()})
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
		if err := e.store.PutResult(ctx, r.request.ProjectID, r.epoch, r.manifest(hash.SectionID), hash.ManifestHash, result); err != nil {
			return Snapshot{}, err
		}
	}
	sections := r.present()
	recordID := RecordID(r.request.ProfileID, r.request.Version, sections)
	record := Record{ID: recordID, ProfileID: r.request.ProfileID, Version: r.request.Version, Sections: sections, Origin: RecordOrigin{AttemptID: attempt, Trigger: r.request.Trigger()}}
	if err := e.store.PutRecord(ctx, r.request.ProjectID, r.epoch, record); err != nil {
		return Snapshot{}, err
	}
	var settled Settled
	var err error
	if attempt != "" {
		settled, err = e.store.CompleteAttempt(ctx, r.request, attempt, r.hashes, recordID)
	} else {
		settled, err = e.store.Resolve(ctx, r.request, r.epoch, r.hashes, recordID)
	}
	if err != nil || !settled.Applied {
		return Snapshot{}, errors.Join(err, errSuperseded)
	}
	snap := r.snapshot(StatusComplete, attempt, recordID, settled.Revision, stored)
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
			return e.generateSection(ctx, r, section, hash)
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
	// Any section left without text fails the attempt; generated sections stay
	// stored, so a retry asks only for the rest.
	if len(failures) > 0 {
		e.fail(settleCtx, r, attempt, Failure{Code: CodeGenerationFailed, Message: strings.Join(failures, "; ")})
		return
	}
	if _, err := e.settle(settleCtx, r, attempt, stored); err != nil && !errors.Is(err, errSuperseded) {
		e.logger.WarnContext(settleCtx, "settle briefing", "profile", r.request.ProfileID, "target", r.request.TargetKey, "error", err)
	}
}

func (e *Engine) generateSection(ctx context.Context, r resolved, section SectionPlan, hash string) (Result, error) {
	request := r.request
	// Another target may have finished this manifest since this one resolved.
	if stored, err := e.store.GetResults(ctx, request.ProjectID, []string{hash}); err == nil {
		if result, ok := stored[hash]; ok && result.Source == SourceSummarizer {
			return result, nil
		}
	}
	contract := section.Manifest.Contract
	if e.generator != nil {
		raw, provider, model, err := e.generator.Generate(ctx, Invocation{ProjectID: request.ProjectID, ProjectDir: request.ProjectDir, Purpose: request.ProfileID, Requested: request.Manual || request.Policy.Admission == Immediate, Prompt: section.Prompt})
		if err == nil {
			if text, parseErr := contract.Parse(observability.RedactCaptureText(raw)); parseErr == nil {
				result := Result{Text: text, Source: SourceSummarizer, Provider: provider, Model: model, CreatedAt: time.Now().UTC()}
				return result, e.store.PutResult(context.WithoutCancel(ctx), request.ProjectID, r.epoch, section.Manifest, hash, result)
			}
		} else if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
	}
	if section.Fallback == "" {
		return Result{}, ErrNoText
	}
	result := Result{Text: section.Fallback, Source: SourceFallback, CreatedAt: time.Now().UTC()}
	return result, e.store.PutResult(context.WithoutCancel(ctx), request.ProjectID, r.epoch, section.Manifest, hash, result)
}

func (e *Engine) fail(ctx context.Context, r resolved, attempt string, failure Failure) {
	request := r.request
	settled, err := e.store.FailAttempt(ctx, request, attempt, failure)
	if err != nil {
		e.logger.WarnContext(ctx, "fail briefing", "profile", request.ProfileID, "target", request.TargetKey, "error", err)
		return
	}
	if settled.Applied && e.publisher != nil {
		snap := Snapshot{ProfileID: request.ProfileID, TargetKey: request.TargetKey, Status: StatusFailed, AttemptID: attempt,
			Epoch: r.epoch, Revision: settled.Revision, ErrorCode: failure.Code, Error: failure.Message}
		e.publisher.PublishSnapshot(ctx, request.ProjectID, snap)
	}
}
