package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

// responseRetryProvider retries empty or interrupted responses within the caller's time budget.
type responseRetryProvider struct {
	inner  modelcall.Provider
	policy providerretry.ProviderHTTPRetry
}

type attemptOutcome struct {
	empty       *failure.ProviderEmptyCompletionError
	interrupted *failure.ProviderResponseInterruptedError
	truncated   *failure.ProviderOutputTruncatedError
	retryable   bool
}

func (p *responseRetryProvider) ID() string                       { return p.inner.ID() }
func (p *responseRetryProvider) Models() []modelcall.ModelInfo    { return p.inner.Models() }
func (p *responseRetryProvider) Profile() providerprofile.Profile { return p.inner.Profile() }

func (p *responseRetryProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	var usage responseRetryUsage
	for attempt := 0; ; attempt++ {
		usage.beginAttempt()
		req.AttemptBudget = &modelcall.CompletionBudget{}
		req.CaptureSources()
		completion, err := p.inner.Complete(ctx, req)
		noteResponseBudget(req, completion)
		if completion != nil {
			usage.observe(completion.Usage, completion.AttemptUsage)
			copied := *completion
			copied.AttemptUsage = usage.attemptUsage()
			copied.Usage = usage.total()
			completion = &copied
		} else if usage.total().Reported() {
			completion = &modelcall.Completion{Usage: usage.total(), AttemptUsage: usage.attemptUsage()}
		}
		usage.finishAttempt()
		if interrupted := p.interruptedCompletion(req, err); interrupted != nil {
			retry, retryErr := p.prepareInterruptedRetry(ctx, attempt, interrupted)
			if retryErr != nil {
				return completion, retryErr
			}
			if !retry {
				return completion, interrupted
			}
			continue
		}
		truncated, isTruncated := failure.AsProviderOutputTruncated(err)
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
			if isTruncated {
				return completion, finalizedTruncatedError(truncated, empty, usage.total())
			}
			return completion, empty
		}
	}
}

func (p *responseRetryProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	for attempt := 0; ; attempt++ {
		attemptCtx, cancel := context.WithCancel(ctx)
		req.AttemptBudget = &modelcall.CompletionBudget{}
		req.CaptureSources()
		first, err := p.inner.Stream(attemptCtx, req)
		if err == nil {
			out := make(chan modelcall.StreamChunk)
			go p.forwardStream(ctx, req, attempt, first, cancel, out)
			return out, nil
		}
		cancel()
		if interrupted := p.interruptedCompletion(req, err); interrupted != nil {
			retry, retryErr := p.prepareInterruptedRetry(ctx, attempt, interrupted)
			if retryErr != nil {
				return nil, retryErr
			}
			if !retry {
				return nil, interrupted
			}
			continue
		}
		truncated, isTruncated := failure.AsProviderOutputTruncated(err)
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
			if isTruncated {
				return nil, finalizedTruncatedError(truncated, empty, modelcall.TokenUsage{})
			}
			return nil, empty
		}
	}
}

func (p *responseRetryProvider) forwardStream(ctx context.Context, req modelcall.CompletionRequest, attempt int, stream <-chan modelcall.StreamChunk, cancel context.CancelFunc, out chan<- modelcall.StreamChunk) {
	defer close(out)
	var usage responseRetryUsage
	var startErr error
	for {
		var outcome attemptOutcome
		if startErr != nil {
			if interrupted := p.interruptedCompletion(req, startErr); interrupted != nil {
				outcome = attemptOutcome{interrupted: interrupted, retryable: true}
			} else if empty, retry := p.emptyCompletion(req, nil, startErr); empty != nil {
				truncated, _ := failure.AsProviderOutputTruncated(startErr)
				outcome = attemptOutcome{empty: empty, truncated: truncated, retryable: retry}
			} else {
				modelcall.SendChunk(ctx, out, modelcall.StreamChunk{Err: startErr, Usage: usage.total(), AttemptUsage: usage.attemptUsage(), Done: true})
				return
			}
		} else {
			outcome = p.forwardAttempt(ctx, req, stream, out, &usage)
			cancel()
			if outcome.empty == nil && outcome.interrupted == nil {
				return
			}
		}
		if outcome.interrupted != nil {
			retry, retryErr := p.prepareInterruptedRetry(ctx, attempt, outcome.interrupted)
			if retryErr != nil {
				modelcall.SendChunk(ctx, out, modelcall.StreamChunk{Err: retryErr, Usage: usage.total(), AttemptUsage: usage.attemptUsage(), Done: true})
				return
			}
			if !retry {
				modelcall.SendChunk(ctx, out, modelcall.StreamChunk{Err: outcome.interrupted, Usage: usage.total(), AttemptUsage: usage.attemptUsage(), Done: true})
				return
			}
		} else {
			retry, retryErr := p.prepareResponseRetry(ctx, &req, attempt, outcome.empty, outcome.retryable)
			if retryErr != nil {
				modelcall.SendChunk(ctx, out, modelcall.StreamChunk{Err: retryErr, Usage: usage.total(), AttemptUsage: usage.attemptUsage(), Done: true})
				return
			}
			if !retry {
				outcome.empty.Attempts = attempt + 1
				outcome.empty.Retryable = false
				var finalErr error = outcome.empty
				if outcome.truncated != nil {
					finalErr = finalizedTruncatedError(outcome.truncated, outcome.empty, usage.total())
				}
				modelcall.SendChunk(ctx, out, modelcall.StreamChunk{Err: finalErr, Usage: usage.total(), AttemptUsage: usage.attemptUsage(), Done: true})
				return
			}
		}
		if !modelcall.SendChunk(ctx, out, modelcall.StreamChunk{Reset: true, ResetReasoning: true, Usage: usage.total(), AttemptUsage: &modelcall.TokenUsage{}}) {
			return
		}
		attempt++
		usage.beginAttempt()
		attemptCtx, nextCancel := context.WithCancel(ctx)
		req.AttemptBudget = &modelcall.CompletionBudget{}
		req.CaptureSources()
		stream, startErr = p.inner.Stream(attemptCtx, req)
		cancel = nextCancel
		if startErr != nil {
			cancel()
		}
	}
}

// forwardAttempt returns a retryable outcome only when no completed tool call
// from this attempt reached the consumer.
func (p *responseRetryProvider) forwardAttempt(ctx context.Context, req modelcall.CompletionRequest, stream <-chan modelcall.StreamChunk, out chan<- modelcall.StreamChunk, usage *responseRetryUsage) attemptOutcome {
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
	hasText, hasToolCalls, hasCompletedToolCalls := false, false, false
	for chunk := range stream {
		usage.observe(chunk.Usage, chunk.AttemptUsage)
		if chunk.Usage.Reported() {
			attemptCompletion.Usage = chunk.Usage
		}
		content.WriteString(chunk.Content)
		if len(chunk.ToolCalls) > 0 {
			attemptCompletion.ToolCalls = chunk.ToolCalls
		}
		if chunk.Usage.Reported() || chunk.Done || chunk.Err != nil {
			chunk.AttemptUsage = usage.attemptUsage()
			chunk.Usage = usage.total()
		}
		hasText = hasText || chunk.Content != ""
		hasToolCalls = hasToolCalls || len(chunk.ToolCalls) > 0
		hasPayload := hasText || hasToolCalls
		if chunk.Done || chunk.Err != nil {
			noteBudget()
		}
		if chunk.Err != nil {
			truncated, _ := failure.AsProviderOutputTruncated(chunk.Err)
			empty, retryable := p.emptyCompletion(req, nil, chunk.Err)
			if empty != nil && !hasPayload {
				return attemptOutcome{empty: empty, truncated: truncated, retryable: retryable}
			}
			chunkHasCompletedToolCall := len(chunk.ToolCalls) > 0 && !chunk.Progress
			if interrupted := p.interruptedCompletion(req, chunk.Err); interrupted != nil && !hasCompletedToolCalls && !chunkHasCompletedToolCall {
				return attemptOutcome{interrupted: interrupted, retryable: true}
			}
			modelcall.SendChunk(ctx, out, chunk)
			return attemptOutcome{}
		}
		if chunk.Done && !hasPayload {
			empty, retryable := p.emptyCompletion(req, nil, nil)
			empty.Terminal = true
			return attemptOutcome{empty: empty, retryable: retryable}
		}
		if !modelcall.SendChunk(ctx, out, chunk) {
			return attemptOutcome{}
		}
		if len(chunk.ToolCalls) > 0 && !chunk.Progress {
			hasCompletedToolCalls = true
		}
		if chunk.Done {
			return attemptOutcome{}
		}
	}
	if !hasText && !hasToolCalls {
		empty, retryable := p.emptyCompletion(req, nil, nil)
		return attemptOutcome{empty: empty, retryable: retryable}
	}
	interruptedErr := failure.InterruptedResponse(ctx, p.ID(), req.Model, io.ErrUnexpectedEOF)
	if interrupted := p.interruptedCompletion(req, interruptedErr); interrupted != nil && !hasCompletedToolCalls {
		return attemptOutcome{interrupted: interrupted, retryable: true}
	}
	modelcall.SendChunk(ctx, out, modelcall.StreamChunk{Err: interruptedErr, Usage: usage.total(), AttemptUsage: usage.attemptUsage(), Done: true})
	return attemptOutcome{}
}

func (p *responseRetryProvider) emptyCompletion(req modelcall.CompletionRequest, completion *modelcall.Completion, err error) (*failure.ProviderEmptyCompletionError, bool) {
	if err != nil {
		if modelcall.CompletionHasPayload(completion) {
			return nil, false
		}
		existing, ok := failure.AsProviderEmptyCompletion(err)
		if truncated, isTruncated := failure.AsProviderOutputTruncated(err); isTruncated && !truncated.ToolCallTruncated && !modelcall.CompletionHasPayload(completion) {
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
	return &failure.ProviderEmptyCompletionError{ProviderID: p.ID(), Model: req.Model, Retryable: true, Terminal: completion != nil}, true
}

func finalizedTruncatedError(truncated *failure.ProviderOutputTruncatedError, empty *failure.ProviderEmptyCompletionError, total modelcall.TokenUsage) *failure.ProviderOutputTruncatedError {
	out := *truncated
	if out.ProviderID == "" && empty != nil {
		out.ProviderID = empty.ProviderID
	}
	if out.Model == "" && empty != nil {
		out.Model = empty.Model
	}
	if out.FinishReason == "" && empty != nil {
		out.FinishReason = empty.Reason
	}
	if empty != nil {
		out.Attempts = empty.Attempts
		out.RecoveryAttempted = empty.RecoveryAttempted
	}
	if total.PromptTokens > 0 {
		out.PromptTokens = total.PromptTokens
	}
	if total.CompletionTokens > 0 {
		out.CompletionTokens = total.CompletionTokens
	}
	return &out
}

func emptyCompletionFault(err error) providerretry.Fault {
	return providerretry.Fault{Kind: providerretry.FaultEmptyCompletion, Err: err}
}

func interruptedFault(err error) providerretry.Fault {
	return providerretry.Fault{Kind: providerretry.FaultInterrupted, Err: err}
}

func (p *responseRetryProvider) interruptedCompletion(req modelcall.CompletionRequest, err error) *failure.ProviderResponseInterruptedError {
	existing, ok := failure.AsProviderResponseInterrupted(err)
	if !ok {
		return nil
	}
	cloned := *existing
	if cloned.ProviderID == "" {
		cloned.ProviderID = p.ID()
	}
	if cloned.Model == "" {
		cloned.Model = req.Model
	}
	return &cloned
}

func (p *responseRetryProvider) prepareInterruptedRetry(ctx context.Context, attempt int, interrupted *failure.ProviderResponseInterruptedError) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	fault := interruptedFault(interrupted)
	if !providerretry.ShouldRetryFault(p.policy, attempt, fault) {
		return false, nil
	}
	if err := providerretry.AwaitFaultRetry(ctx, p.policy, attempt, fault); err != nil {
		return false, err
	}
	return true, nil
}

var _ modelcall.Provider = (*responseRetryProvider)(nil)

// Output-limit recovery lowers reasoning once within the caller's time budget.
func (p *responseRetryProvider) prepareResponseRetry(ctx context.Context, req *modelcall.CompletionRequest, attempt int, empty *failure.ProviderEmptyCompletionError, retryable bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	empty.RecoveryAttempted = req.ReasoningRecovery
	if empty.OutputLimitReached() {
		if req.ReasoningRecovery || req.ThinkingOverride != nil || req.AttemptBudget == nil || !req.AttemptBudget.CanReduceReasoning {
			return false, nil
		}
		req.ReasoningRecovery = true
		modelcall.MarkSessionReasoningRecovery(req.Debug.SessionID)
		slog.InfoContext(ctx, "recovering empty output-limit response with lower reasoning", "provider", p.ID(), "model", req.Model, "attempt", attempt+2)
		providerretry.ObserveRetry(ctx, providerretry.RetryAttempt{Attempt: attempt + 2, MaxAttempts: attempt + 2, Reason: providerretry.RetryReasonEmptyCompletion})
		return true, ctx.Err()
	}
	if !retryable || !providerretry.ShouldRetryFault(p.policy, attempt, emptyCompletionFault(empty)) {
		return false, nil
	}
	if err := providerretry.AwaitFaultRetry(ctx, p.policy, attempt, emptyCompletionFault(empty)); err != nil {
		return false, err
	}
	if empty.Terminal {
		if err := p.appendEmptyResponseRecovery(ctx, req); err != nil {
			return false, err
		}
	}
	return true, nil
}

func noteResponseBudget(req modelcall.CompletionRequest, completion *modelcall.Completion) {
	if completion == nil || req.AttemptBudget == nil || req.ThinkingOverride != nil {
		return
	}
	if req.AttemptBudget.ReducedReasoning && modelcall.CompletionHasPayload(completion) {
		modelcall.ClearSessionReasoningRecovery(strings.TrimSpace(req.Debug.SessionID))
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

// Recovery changes only the suffix of the already admitted request. It never
// creates an additional retry allowance or includes provider output.
func (p *responseRetryProvider) appendEmptyResponseRecovery(ctx context.Context, req *modelcall.CompletionRequest) error {
	render := req.EmptyResponseRecovery
	req.EmptyResponseRecovery = nil
	if render == nil {
		return nil
	}
	body, err := render(ctx)
	if err != nil || strings.TrimSpace(body) == "" {
		return err
	}
	if len(body) > 4096 {
		return fmt.Errorf("empty response recovery exceeds 4096 bytes")
	}
	candidate := *req
	candidate.Messages = append(slices.Clone(req.Messages), api.Message{Role: api.MessageRoleUser, Content: body, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted, Visibility: api.MessageVisibilityInternal})
	if p.admitEmptyResponseRecovery(ctx, candidate) {
		req.Messages = candidate.Messages
	}
	return nil
}

func (p *responseRetryProvider) admitEmptyResponseRecovery(ctx context.Context, req modelcall.CompletionRequest) bool {
	var budget modelcall.RequestBudget
	if reporter, ok := p.inner.(modelcall.RequestBudgetReporter); ok {
		budget = reporter.RequestBudget(ctx, req)
	}
	budget.MinimumOutput = max(budget.MinimumOutput, modelcall.MinimumCompletionOutput(req))
	reserve := max(budget.Output, budget.MinimumOutput)
	return budget.Err == nil && budget.Window > 0 && budget.Input > 0 && reserve > 0 && budget.Headroom >= 0 &&
		budget.Output >= budget.MinimumOutput && budget.Input+reserve+budget.Headroom <= budget.Window
}
