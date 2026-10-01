package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/tokenest"
)

// responseRetryProvider retries empty responses within the caller's time budget.
type responseRetryProvider struct {
	inner  modelcall.Provider
	policy providerretry.ProviderHTTPRetry
}

func (p *responseRetryProvider) ID() string                       { return p.inner.ID() }
func (p *responseRetryProvider) Models() []modelcall.ModelInfo    { return p.inner.Models() }
func (p *responseRetryProvider) Profile() providerprofile.Profile { return p.inner.Profile() }

func (p *responseRetryProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	var usage responseRetryUsage
	for attempt := 0; ; attempt++ {
		req.AttemptBudget = &modelcall.CompletionBudget{}
		completion, err := p.inner.Complete(ctx, req)
		noteResponseBudget(req, completion)
		if completion != nil {
			usage.observe(completion.Usage)
			copied := *completion
			copied.Usage = usage.total()
			completion = &copied
		} else if usage.total().Reported() {
			completion = &modelcall.Completion{Usage: usage.total()}
		}
		usage.finishAttempt()
		empty, retryable := p.emptyCompletion(req, completion, err)
		if empty == nil {
			return completion, err
		}
		retry, retryErr := p.prepareResponseRetry(ctx, &req, attempt, empty, retryable)
		if retryErr != nil {
			return completion, retryErr
		}
		if !retry {
			empty.Attempts = attempt + 1
			empty.Retryable = false
			return completion, empty
		}
	}
}

func (p *responseRetryProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	for attempt := 0; ; attempt++ {
		attemptCtx, cancel := context.WithCancel(ctx)
		req.AttemptBudget = &modelcall.CompletionBudget{}
		first, err := p.inner.Stream(attemptCtx, req)
		if err == nil {
			out := make(chan modelcall.StreamChunk)
			go p.forwardStream(ctx, req, attempt, first, cancel, out)
			return out, nil
		}
		cancel()
		empty, retryable := p.emptyCompletion(req, nil, err)
		if empty == nil {
			return nil, err
		}
		retry, retryErr := p.prepareResponseRetry(ctx, &req, attempt, empty, retryable)
		if retryErr != nil {
			return nil, retryErr
		}
		if !retry {
			empty.Attempts = attempt + 1
			empty.Retryable = false
			return nil, empty
		}
	}
}

func (p *responseRetryProvider) forwardStream(
	ctx context.Context,
	req modelcall.CompletionRequest,
	attempt int,
	stream <-chan modelcall.StreamChunk,
	cancel context.CancelFunc,
	out chan<- modelcall.StreamChunk,
) {
	defer close(out)

	var usage responseRetryUsage
	var startErr error
	for {
		var empty *failure.ProviderEmptyCompletionError
		var retry bool
		if startErr != nil {
			empty, retry = p.emptyCompletion(req, nil, startErr)
			if empty == nil {
				modelcall.SendChunk(ctx, out, modelcall.StreamChunk{Err: startErr, Usage: usage.total(), Done: true})
				return
			}
		} else {
			empty, retry = p.forwardAttempt(ctx, req, stream, out, &usage)
			cancel()
			if empty == nil {
				return
			}
		}
		retry, retryErr := p.prepareResponseRetry(ctx, &req, attempt, empty, retry)
		if retryErr != nil {
			modelcall.SendChunk(ctx, out, modelcall.StreamChunk{Err: retryErr, Usage: usage.total(), Done: true})
			return
		}
		if !retry {
			empty.Attempts = attempt + 1
			empty.Retryable = false
			modelcall.SendChunk(ctx, out, modelcall.StreamChunk{Err: empty, Usage: usage.total(), Done: true})
			return
		}
		if !modelcall.SendChunk(ctx, out, modelcall.StreamChunk{ResetReasoning: true, Usage: usage.total()}) {
			return
		}
		attempt++
		attemptCtx, nextCancel := context.WithCancel(ctx)
		req.AttemptBudget = &modelcall.CompletionBudget{}
		stream, startErr = p.inner.Stream(attemptCtx, req)
		cancel = nextCancel
		if startErr != nil {
			cancel()
		}
	}
}

// forwardAttempt returns a typed empty completion only when replay is safe:
// no visible content or tool call from this attempt reached the consumer.
func (p *responseRetryProvider) forwardAttempt(
	ctx context.Context,
	req modelcall.CompletionRequest,
	stream <-chan modelcall.StreamChunk,
	out chan<- modelcall.StreamChunk,
	usage *responseRetryUsage,
) (*failure.ProviderEmptyCompletionError, bool) {
	defer usage.finishAttempt()
	var attemptCompletion modelcall.Completion
	var content strings.Builder
	noted := false
	noteBudget := func() {
		if noted {
			return
		}
		noted = true
		attemptCompletion.Content = content.String()
		noteResponseBudget(req, &attemptCompletion)
	}
	defer noteBudget()
	hasText := false
	hasToolCalls := false
	for chunk := range stream {
		usage.observe(chunk.Usage)
		if chunk.Usage.Reported() {
			attemptCompletion.Usage = chunk.Usage
		}
		content.WriteString(chunk.Content)
		if len(chunk.ToolCalls) > 0 {
			attemptCompletion.ToolCalls = chunk.ToolCalls
		}
		if chunk.Usage.Reported() || chunk.Done || chunk.Err != nil {
			chunk.Usage = usage.total()
		}
		hasText = hasText || chunk.Content != ""
		hasToolCalls = hasToolCalls || len(chunk.ToolCalls) > 0
		hasPayload := hasText || hasToolCalls
		if chunk.Done || chunk.Err != nil {
			noteBudget()
		}
		if chunk.Err != nil {
			empty, retryable := p.emptyCompletion(req, nil, chunk.Err)
			if empty != nil && !hasPayload {
				return empty, retryable
			}
			modelcall.SendChunk(ctx, out, chunk)
			return nil, false
		}
		if chunk.Done && !hasPayload {
			empty, retryable := p.emptyCompletion(req, nil, nil)
			empty.Terminal = true
			return empty, retryable
		}
		if !modelcall.SendChunk(ctx, out, chunk) {
			return nil, false
		}
		if chunk.Done {
			return nil, false
		}
	}
	if !hasText && !hasToolCalls {
		empty, retryable := p.emptyCompletion(req, nil, nil)
		return empty, retryable
	}
	modelcall.SendChunk(ctx, out, modelcall.StreamChunk{
		Err:  fmt.Errorf("provider %s/%s stream closed before a terminal event", p.ID(), req.Model),
		Done: true,
	})
	return nil, false
}

func (p *responseRetryProvider) emptyCompletion(
	req modelcall.CompletionRequest,
	completion *modelcall.Completion,
	err error,
) (*failure.ProviderEmptyCompletionError, bool) {
	if err != nil {
		if modelcall.CompletionHasPayload(completion) {
			return nil, false
		}
		existing, ok := failure.AsProviderEmptyCompletion(err)
		if truncated, isTruncated := failure.AsProviderOutputTruncated(err); isTruncated && !modelcall.CompletionHasPayload(completion) {
			existing = &failure.ProviderEmptyCompletionError{ProviderID: truncated.ProviderID, Model: truncated.Model, Reason: truncated.FinishReason, Terminal: true}
			ok = true
		}
		if !ok {
			return nil, false
		}
		cloned := *existing
		if cloned.ProviderID == "" {
			cloned.ProviderID = p.ID()
		}
		if cloned.Model == "" {
			cloned.Model = req.Model
		}
		return &cloned, cloned.Retryable
	}
	if modelcall.CompletionHasPayload(completion) {
		return nil, false
	}
	return &failure.ProviderEmptyCompletionError{
		ProviderID: p.ID(),
		Model:      req.Model,
		Retryable:  true,
		Terminal:   completion != nil,
	}, true
}

func emptyCompletionFault(err error) providerretry.Fault {
	return providerretry.Fault{Kind: providerretry.FaultEmptyCompletion, Err: err}
}

var _ modelcall.Provider = (*responseRetryProvider)(nil)

// Output-limit recovery lowers reasoning once within the caller's time budget.
func (p *responseRetryProvider) prepareResponseRetry(ctx context.Context, req *modelcall.CompletionRequest, attempt int, empty *failure.ProviderEmptyCompletionError, retryable bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	empty.RecoveryAttempted = req.StrictBudget
	if empty.OutputLimitReached() {
		if req.StrictBudget || req.ThinkingOverride != nil || req.AttemptBudget == nil || !req.AttemptBudget.CanReduceReasoning {
			return false, nil
		}
		req.StrictBudget = true
		modelcall.MarkSessionStrictBudget(req.Debug.SessionID)
		if req.MaxTokens > modelcall.StrictMaxTokens {
			req.MaxTokens = modelcall.StrictMaxTokens
		}
		slog.InfoContext(ctx, "recovering empty output-limit response with lower reasoning", "provider", p.ID(), "model", req.Model, "attempt", attempt+2)
		providerretry.ObserveRetry(ctx, providerretry.RetryAttempt{Attempt: attempt + 2, MaxAttempts: attempt + 2, Reason: providerretry.RetryReasonEmptyCompletion})
		return true, ctx.Err()
	}
	if !retryable || !providerretry.ShouldRetryFault(p.policy, attempt, emptyCompletionFault(empty)) {
		return false, nil
	}
	return true, providerretry.AwaitFaultRetry(ctx, p.policy, attempt, emptyCompletionFault(empty))
}

func noteResponseBudget(req modelcall.CompletionRequest, completion *modelcall.Completion) {
	if completion == nil || req.AttemptBudget == nil || req.ThinkingOverride != nil {
		return
	}
	if req.AttemptBudget.Strict && modelcall.CompletionHasPayload(completion) {
		modelcall.ClearSessionStrictBudget(strings.TrimSpace(req.Debug.SessionID))
	}
	visibleBytes := len(completion.Content)
	for _, call := range completion.ToolCalls {
		if raw, err := json.Marshal(call.Args); err == nil && call.Args != nil {
			visibleBytes += len(raw)
		}
	}
	visible := tokenest.FromUnitCount(visibleBytes, tokenest.DefaultDivisor)
	modelcall.NoteCompletionTurnBudget(req.Debug.SessionID, req.AttemptBudget.MaxTokens, completion.Usage.CompletionTokens, visible)
}
