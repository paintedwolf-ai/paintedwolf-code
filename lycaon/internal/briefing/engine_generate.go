package briefing

import (
	"context"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
)

func (e *Engine) generate(ctx context.Context, r resolved, attempt string, stored map[string]Result) {
	parentCtx := ctx
	runCtx := ctx
	if limit := r.request.Policy.RunLimit; limit > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
	}
	var failures []*SectionFailureError
	for idx, i := range r.missing {
		if runCtx.Err() != nil {
			if errors.Is(parentCtx.Err(), context.Canceled) {
				e.fail(context.WithoutCancel(parentCtx), r, attempt, Failure{Code: CodeCancelled})
				return
			}
			for _, rem := range r.missing[idx:] {
				failures = append(failures, classifyGenerationError(r.request.Sections[rem].ID, runCtx.Err(), runCtx.Err()))
			}
			break
		}
		section := r.request.Sections[i]
		hash := r.hashes[i].ManifestHash
		result, err := e.flights.do(runCtx, r.request.ProjectID+"\x00"+hash, func() (Result, error) {
			return e.generateSection(runCtx, r, section, hash)
		})
		if err != nil {
			if errors.Is(parentCtx.Err(), context.Canceled) {
				e.fail(context.WithoutCancel(parentCtx), r, attempt, Failure{Code: CodeCancelled})
				return
			}
			var secErr *SectionFailureError
			if !errors.As(err, &secErr) {
				secErr = classifyGenerationError(section.ID, err, runCtx.Err())
			}
			failures = append(failures, secErr)
			continue
		}
		r.results[hash] = result
	}
	settleCtx := context.WithoutCancel(parentCtx)
	// Any section left without text fails the attempt; generated sections stay
	// stored, so a retry asks only for the rest.
	if len(failures) > 0 {
		e.fail(settleCtx, r, attempt, Failure{Code: CodeGenerationFailed, Message: formatSectionFailures(failures)})
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
		raw, provider, model, genErr := e.generator.Generate(ctx, Invocation{
			ProjectID: request.ProjectID, ProjectDir: request.ProjectDir, Purpose: request.ProfileID,
			Requested: request.Manual || request.Policy.Admission == Immediate, Prompt: section.Prompt,
		})
		if genErr == nil {
			text, parseErr := contract.Parse(observability.RedactCaptureText(raw))
			if parseErr == nil {
				result := Result{Text: text, Source: SourceSummarizer, Provider: provider, Model: model, Quality: section.Quality, CreatedAt: time.Now().UTC()}
				return result, e.store.PutResult(context.WithoutCancel(ctx), request.ProjectID, r.epoch, section.Manifest, hash, result)
			}
			if section.Fallback == "" {
				return Result{}, classifyParseError(section.ID, parseErr)
			}
		} else if errors.Is(ctx.Err(), context.Canceled) || section.Fallback == "" {
			return Result{}, classifyGenerationError(section.ID, genErr, ctx.Err())
		}
	} else if section.Fallback == "" {
		return Result{}, &SectionFailureError{
			SectionID: section.ID,
			Stage:     StageAdmission,
			Kind:      FailureUnavailableGenerator,
			Summary:   "admission failed (utility model unavailable)",
		}
	}
	result := Result{Text: section.Fallback, Source: SourceFallback, Quality: section.Quality, CreatedAt: time.Now().UTC()}
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
