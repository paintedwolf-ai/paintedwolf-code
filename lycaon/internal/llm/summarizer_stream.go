package llm

import (
	"context"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/pkg/api"
)

// StreamingSummarizer delivers completion fragments incrementally.
type StreamingSummarizer interface {
	compaction.Summarizer
	// SummarizeStream delivers fragments and returns the full content.
	SummarizeStream(ctx context.Context, systemPrompt, userPrompt string, maxTokens int, onDelta func(delta string)) (string, error)
}

// SummarizeStream streams a lite-model completion with blocking fallback.
func (r *RegistrySummarizer) SummarizeStream(ctx context.Context, systemPrompt, userPrompt string, maxTokens int, onDelta func(delta string)) (string, error) {
	return r.summarizeStream(ctx, systemPrompt, userPrompt, maxTokens, onDelta, true)
}

// SummarizeStreamOnce performs one streaming lite-model attempt.
func (r *RegistrySummarizer) SummarizeStreamOnce(ctx context.Context, systemPrompt, userPrompt string, maxTokens int, onDelta func(delta string)) (string, error) {
	return r.summarizeStream(ctx, systemPrompt, userPrompt, maxTokens, onDelta, false)
}

func (r *RegistrySummarizer) summarizeStream(ctx context.Context, systemPrompt, userPrompt string, maxTokens int, onDelta func(delta string), allowFallback bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p, model, err := r.liteProvider(ctx)
	if err != nil || p == nil {
		if allowFallback && ctx.Err() == nil && !SecretScreenBlocked(err) {
			return r.Summarize(ctx, systemPrompt, userPrompt, maxTokens)
		}
		if err != nil {
			return "", preserveCallerContextError(err, ctx.Err())
		}
		return "", newUnconfiguredUtilityError("no configured provider")
	}
	if r.Plane != nil && !r.Plane.allowLite() {
		if allowFallback && ctx.Err() == nil {
			return r.Summarize(ctx, systemPrompt, userPrompt, maxTokens)
		}
		return "", ErrLiteUnavailable
	}
	callCtx, release, laneErr := r.planeAcquire(ctx, p, r.utilityClass())
	if laneErr != nil {
		if allowFallback && ctx.Err() == nil && !SecretScreenBlocked(laneErr) {
			return r.Summarize(ctx, systemPrompt, userPrompt, maxTokens)
		}
		return "", preserveCallerContextError(laneErr, ctx.Err())
	}
	out := r.streamLite(callCtx, p, model, systemPrompt, userPrompt, maxTokens, onDelta)
	callErr := callCtx.Err()
	release()
	if out.setupErr != nil {
		if callErr != nil || ctx.Err() != nil || SecretScreenBlocked(out.setupErr) || !allowFallback {
			return out.content, preserveCallerContextError(out.setupErr, firstNonNilErr(callErr, ctx.Err()))
		}
		return r.Summarize(ctx, systemPrompt, userPrompt, maxTokens)
	}
	if callErr != nil {
		return out.content, preserveCallerContextError(out.streamErr, callErr)
	}
	if out.streamErr != nil {
		return out.content, out.streamErr
	}
	return out.content, nil
}

func firstNonNilErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

type streamLiteResult struct {
	content   string
	setupErr  error
	streamErr error
}

func (r *RegistrySummarizer) streamLite(ctx context.Context, p modelcall.Provider, model, systemPrompt, userPrompt string, maxTokens int, onDelta func(delta string)) streamLiteResult {
	observe := r.observingLiteSlot(ctx, p)
	today, todayErr := guidance.RenderTodayLine(ctx, time.Now(), ResolveModelCutoff(model))
	if todayErr != nil {
		return streamLiteResult{setupErr: todayErr}
	}
	req := modelcall.CompletionRequest{
		Model: model,
		Messages: transcript.Project([]api.Message{
			{Role: api.MessageRoleSystem, Content: today + "\n\n" + systemPrompt, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted},
			{Role: api.MessageRoleUser, Content: userPrompt, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted},
		}),
		Debug:     modelcall.RequestDebug{Purpose: r.Purpose},
		Think:     modelcall.ThinkOff,
		MaxTokens: r.answerTokens(ctx, p, model, maxTokens),
	}
	req = r.utilityRequest(ctx, req)
	attempt := r.beginUtilityCall(ctx, p, model)
	defer attempt.close()
	callID, occupy := r.beginLaneCall(ctx, attempt.callID, attempt.provider, model)
	chunks, err := p.Stream(attempt.ctx, req) //nolint:contextcheck // attempt.ctx is ctx plus this provider's budget
	if err != nil {
		if errors.Is(attempt.ctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			err = &utilityBudgetExceededError{cause: err}
		}
		attempt.void()
		r.noteCallOutcome(observe, providerIDOf(p), model, err)
		r.endLaneCall(ctx, occupy, callID, attempt.provider, model, &err)
		return streamLiteResult{setupErr: err}
	}
	collected := &modelcall.Completion{}
	var content []byte
	var streamErr error
	for chunk := range chunks {
		if chunk.ResetAttempt() {
			content = nil
		}
		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			collected.Usage = chunk.Usage
		}
		if chunk.Err != nil {
			streamErr = chunk.Err
			break
		}
		if chunk.Content != "" {
			content = append(content, chunk.Content...)
			if onDelta != nil {
				onDelta(chunk.Content)
			}
		}
		if chunk.Done {
			break
		}
	}
	collected.Content = string(content)
	attempt.finish(req, collected)
	if streamErr == nil {
		streamErr = attempt.ctx.Err()
	}
	if streamErr != nil && errors.Is(attempt.ctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		streamErr = &utilityBudgetExceededError{cause: streamErr}
	}
	r.noteCallOutcome(observe, providerIDOf(p), model, streamErr)
	r.endLaneCall(ctx, occupy, callID, attempt.provider, model, &streamErr)
	return streamLiteResult{content: collected.Content, streamErr: streamErr}
}

var _ StreamingSummarizer = (*RegistrySummarizer)(nil)
