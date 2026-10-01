package llm

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func captureLLMComplete(providerID string, profile providerprofile.Profile, req modelcall.CompletionRequest, call func() (*modelcall.Completion, error)) (*modelcall.Completion, error) {
	start := time.Now()
	out, err := call()
	elapsed := time.Since(start).Milliseconds()
	var usage observability.LLMUsageCapture
	var summary *observability.LLMCompletionCapture
	if out != nil {
		usage = observability.LLMUsageCapture{
			PromptTokens:               out.Usage.PromptTokens,
			CompletionTokens:           out.Usage.CompletionTokens,
			CacheReadInputTokens:       out.Usage.CacheReadInputTokens,
			CacheCreationInputTokens:   out.Usage.CacheCreationInputTokens,
			Present:                    out.Usage.Present,
			Incomplete:                 out.Usage.Incomplete,
			CacheCreation1HInputTokens: out.Usage.CacheCreation1HInputTokens,
		}
		summary = observability.SummarizeLLMCompletion(out.Content, out.ToolCalls)
		var reasoning reasoningCapture
		reasoning.append(out.Reasoning)
		summary = reasoning.summarize(summary, err)
	}
	var cacheUsage modelcall.TokenUsage
	if out != nil {
		cacheUsage = out.Usage
	}
	providerwire.NotePromptCacheUsage(providerID, req, profile, cacheUsage, start, err != nil)
	callLabel := "complete"
	if purpose := strings.TrimSpace(req.Debug.Purpose); purpose != "" {
		callLabel = purpose
	}
	observability.LogLLMRequest(providerID, req.Model, callLabel, req.Messages, captureTools(req.Tools), observability.LLMRequestTiming{
		DurationMs: elapsed,
		TTFTMs:     elapsed,
	}, usage, observability.LLMRequestDebug{
		CallID:            req.Debug.CallID,
		ReasoningRecovery: req.StrictBudget,
		RequestControls:   req.ControlCapture.Snapshot(),
		EmptyCompletion:   captureEmptyCompletion(err),
		SessionID:         req.Debug.SessionID,
		AgentType:         req.Debug.AgentType,
		ParentSessionID:   req.Debug.ParentSessionID,
		ProfileID:         req.Debug.ProfileID,
		HostTurn:          req.Debug.HostTurn,
		Surface:           req.Debug.Surface,
		Iteration:         req.Debug.Iteration,
		MaxIterations:     req.Debug.MaxIterations,
		WorkflowRevision:  req.Debug.WorkflowRevision,
	}, summary, errString(err))
	return out, err
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func captureLLMStream(ctx context.Context, providerID string, profile providerprofile.Profile, req modelcall.CompletionRequest, call func() (<-chan modelcall.StreamChunk, error)) (<-chan modelcall.StreamChunk, error) {
	start := time.Now()
	ch, err := call()
	if err != nil {
		// Capture failures that happen before a stream opens.
		providerwire.NotePromptCacheUsage(providerID, req, profile, modelcall.TokenUsage{}, start, true)
		logStreamCapture(providerID, req, start, 0, observability.LLMUsageCapture{}, nil, err)
		return nil, err
	}
	out := make(chan modelcall.StreamChunk, 64)
	go func() {
		defer close(out)
		var ttftMs int64
		var ttftRecorded bool
		var usage observability.LLMUsageCapture
		var content strings.Builder
		var toolCalls []api.ToolCall
		var streamErr error
		var reasoning reasoningCapture
		for chunk := range ch {
			reasoning.append(chunk.Reasoning)
			if chunk.Err != nil {
				streamErr = chunk.Err
			}
			if !ttftRecorded && streamChunkHasOutput(chunk) {
				ttftMs = time.Since(start).Milliseconds()
				ttftRecorded = true
			}
			if chunk.Content != "" {
				content.WriteString(chunk.Content)
			}
			if len(chunk.ToolCalls) > 0 {
				toolCalls = chunk.ToolCalls
			}
			if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 ||
				chunk.Usage.CacheReadInputTokens > 0 || chunk.Usage.CacheCreationInputTokens > 0 {
				usage = observability.LLMUsageCapture{
					PromptTokens:               chunk.Usage.PromptTokens,
					CompletionTokens:           chunk.Usage.CompletionTokens,
					CacheReadInputTokens:       chunk.Usage.CacheReadInputTokens,
					CacheCreationInputTokens:   chunk.Usage.CacheCreationInputTokens,
					Present:                    chunk.Usage.Present,
					Incomplete:                 chunk.Usage.Incomplete,
					CacheCreation1HInputTokens: chunk.Usage.CacheCreation1HInputTokens,
				}
			}
			if !modelcall.SendChunk(ctx, out, chunk) {
				// Drain abandoned streams before recording the partial response.
				modelcall.DrainStream(ch)
				break
			}
		}
		summary := reasoning.summarize(observability.SummarizeLLMCompletion(content.String(), toolCalls), streamErr)
		providerwire.NotePromptCacheUsage(providerID, req, profile, modelcall.TokenUsage{
			PromptTokens:               usage.PromptTokens,
			CompletionTokens:           usage.CompletionTokens,
			CacheReadInputTokens:       usage.CacheReadInputTokens,
			CacheCreationInputTokens:   usage.CacheCreationInputTokens,
			Present:                    usage.Present,
			Incomplete:                 usage.Incomplete,
			CacheCreation1HInputTokens: usage.CacheCreation1HInputTokens,
		}, start, streamErr != nil || ctx.Err() != nil)
		logStreamCapture(providerID, req, start, ttftMs, usage, summary, streamErr)
	}()
	return out, nil
}

func logStreamCapture(providerID string, req modelcall.CompletionRequest, start time.Time, ttftMs int64, usage observability.LLMUsageCapture, summary *observability.LLMCompletionCapture, responseErr error) {
	// Purpose identifies utility streams in the capture.
	callLabel := "stream"
	if purpose := strings.TrimSpace(req.Debug.Purpose); purpose != "" {
		callLabel = purpose
	}
	observability.LogLLMRequest(providerID, req.Model, callLabel, req.Messages, captureTools(req.Tools), observability.LLMRequestTiming{
		DurationMs: time.Since(start).Milliseconds(),
		TTFTMs:     ttftMs,
	}, usage, observability.LLMRequestDebug{
		CallID:            req.Debug.CallID,
		ReasoningRecovery: req.StrictBudget,
		RequestControls:   req.ControlCapture.Snapshot(),
		EmptyCompletion:   captureEmptyCompletion(responseErr),
		SessionID:         req.Debug.SessionID,
		AgentType:         req.Debug.AgentType,
		ParentSessionID:   req.Debug.ParentSessionID,
		ProfileID:         req.Debug.ProfileID,
		HostTurn:          req.Debug.HostTurn,
		Surface:           req.Debug.Surface,
		Iteration:         req.Debug.Iteration,
		MaxIterations:     req.Debug.MaxIterations,
		WorkflowRevision:  req.Debug.WorkflowRevision,
	}, summary, errString(responseErr))
}

func captureTools(toolMetas []tools.ToolMeta) []observability.LLMToolCapture {
	if len(toolMetas) == 0 {
		return nil
	}
	result := make([]observability.LLMToolCapture, 0, len(toolMetas))
	for _, meta := range toolMetas {
		result = append(result, observability.LLMToolCapture{
			Name:              meta.Name,
			Description:       meta.Description,
			ArgsSchema:        meta.ArgsSchema,
			ApprovalCategory:  meta.ApprovalCategory,
			ApprovalSubject:   meta.ApprovalSubject,
			UntrustedMetadata: meta.UntrustedMetadata,
			Deferred:          meta.Deferred,
			ReadOnlyHint:      meta.ReadOnlyHint,
		})
	}
	return result
}

func streamChunkHasOutput(chunk modelcall.StreamChunk) bool {
	return chunk.Content != "" || chunk.Reasoning != "" || len(chunk.ToolCalls) > 0
}

func captureEmptyCompletion(err error) *observability.LLMEmptyCompletionCapture {
	empty, ok := failure.AsProviderEmptyCompletion(err)
	if !ok {
		return nil
	}
	return &observability.LLMEmptyCompletionCapture{Terminal: empty.Terminal, Retryable: empty.Retryable, Attempts: empty.Attempts, Reason: observability.RedactCaptureText(empty.Reason)}
}
