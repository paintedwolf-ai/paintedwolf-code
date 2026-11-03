package modelcall

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/pkg/api"
)

// LowOutputCeiling reports whether entry's configured max_tokens is below the provider ceiling.
func LowOutputCeiling(entry modelinfo.Entry) (configured, providerCeiling int, low bool) {
	if entry.MaxTokens <= 0 {
		return 0, 0, false
	}
	ceiling := entry.ProviderMaxTokens
	if ceiling == 0 && entry.MaxTokens < OrchestrationMaxTokens {
		ceiling = OrchestrationMaxTokens
	}
	if ceiling > entry.MaxTokens {
		return entry.MaxTokens, ceiling, true
	}
	return entry.MaxTokens, ceiling, false
}

// FormatLowOutputCeilingDiagnostic formats a configuration hint for a capped model output ceiling.
func FormatLowOutputCeilingDiagnostic(model string, configured, providerCeiling int) string {
	if configured <= 0 || providerCeiling <= configured {
		return ""
	}
	subject := "The model entry"
	if trimmed := strings.TrimSpace(model); trimmed != "" {
		subject = fmt.Sprintf("Model %q", trimmed)
	}
	return fmt.Sprintf(
		"%s is configured with max_tokens=%d, below the provider output ceiling of %d tokens; raise max_tokens in provider configuration.",
		subject, configured, providerCeiling,
	)
}

// FirstTruncatedToolCall returns the first tool call whose JSON arguments were truncated on length.
func FirstTruncatedToolCall(calls []api.ToolCall) (api.ToolCall, bool) {
	for _, call := range calls {
		if call.ArgsTruncated {
			return call, true
		}
	}
	return api.ToolCall{}, false
}

// HasTruncatedToolCall reports whether any tool call in calls had its arguments truncated on length.
func HasTruncatedToolCall(calls []api.ToolCall) bool {
	_, ok := FirstTruncatedToolCall(calls)
	return ok
}

// CompletionHasUsablePayload reports visible prose or at least one non-truncated tool call.
func CompletionHasUsablePayload(completion *Completion) bool {
	if completion == nil {
		return false
	}
	if strings.TrimSpace(completion.Content) != "" {
		return true
	}
	for _, call := range completion.ToolCalls {
		if !call.ArgsTruncated {
			return true
		}
	}
	return false
}

// EffectiveOutputMaxTokens resolves the output token limit applied to req.
func EffectiveOutputMaxTokens(req CompletionRequest, entry modelinfo.Entry) int {
	if req.AttemptBudget != nil && req.AttemptBudget.MaxTokens > 0 {
		return req.AttemptBudget.MaxTokens
	}
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	if entry.MaxTokens > 0 {
		return entry.MaxTokens
	}
	return 0
}

// NewOutputTruncatedError constructs a structured output-budget fault.
func NewOutputTruncatedError(
	providerID, model, reason string,
	usage TokenUsage,
	maxTokens int,
	entry modelinfo.Entry,
	calls []api.ToolCall,
) *failure.ProviderOutputTruncatedError {
	if maxTokens <= 0 && entry.MaxTokens > 0 {
		maxTokens = entry.MaxTokens
	}
	configured, ceiling, low := LowOutputCeiling(entry)
	out := &failure.ProviderOutputTruncatedError{
		ProviderID:       providerID,
		Model:            model,
		FinishReason:     strings.TrimSpace(reason),
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ContextTokens:    entry.ContextLength,
		MaxTokens:        maxTokens,
	}
	if low {
		out.ConfiguredMaxTokens = configured
		out.ProviderMaxTokens = ceiling
	} else if entry.MaxTokens > 0 {
		out.ConfiguredMaxTokens = entry.MaxTokens
		out.ProviderMaxTokens = entry.ProviderMaxTokens
	}
	if tc, ok := FirstTruncatedToolCall(calls); ok {
		out.ToolCallTruncated = true
		out.ToolName = strings.TrimSpace(tc.Name)
		out.ToolArgsBytes = tc.TruncatedArgsBytes()
	}
	return out
}

// EnrichOutputTruncatedError fills missing ceiling and truncated tool-call facts onto err.
func EnrichOutputTruncatedError(
	err *failure.ProviderOutputTruncatedError,
	req CompletionRequest,
	entry modelinfo.Entry,
	completion *Completion,
) *failure.ProviderOutputTruncatedError {
	if err == nil {
		return nil
	}
	cloned := *err
	if cloned.MaxTokens <= 0 {
		cloned.MaxTokens = EffectiveOutputMaxTokens(req, entry)
	}
	if cloned.ContextTokens <= 0 && entry.ContextLength > 0 {
		cloned.ContextTokens = entry.ContextLength
	}
	if cloned.ConfiguredMaxTokens <= 0 && entry.MaxTokens > 0 {
		configured, ceiling, low := LowOutputCeiling(entry)
		if low {
			cloned.ConfiguredMaxTokens = configured
			cloned.ProviderMaxTokens = ceiling
		} else {
			cloned.ConfiguredMaxTokens = entry.MaxTokens
			cloned.ProviderMaxTokens = entry.ProviderMaxTokens
		}
	}
	if completion != nil {
		if cloned.ProviderID == "" {
			cloned.ProviderID = completion.ProviderID
		}
		if cloned.Model == "" {
			cloned.Model = completion.Model
		}
		if cloned.FinishReason == "" {
			cloned.FinishReason = strings.TrimSpace(completion.FinishReason)
		}
		if cloned.PromptTokens == 0 && completion.Usage.PromptTokens > 0 {
			cloned.PromptTokens = completion.Usage.PromptTokens
		}
		if cloned.CompletionTokens == 0 && completion.Usage.CompletionTokens > 0 {
			cloned.CompletionTokens = completion.Usage.CompletionTokens
		}
		if !cloned.ToolCallTruncated {
			if tc, ok := FirstTruncatedToolCall(completion.ToolCalls); ok {
				cloned.ToolCallTruncated = true
				cloned.ToolName = strings.TrimSpace(tc.Name)
				cloned.ToolArgsBytes = tc.TruncatedArgsBytes()
			}
		}
	}
	return &cloned
}

// EmptyOrTruncatedOutputError classifies finish_reason: length with no usable content
// as ProviderOutputTruncatedError and other empty completions as ProviderEmptyCompletionError.
func EmptyOrTruncatedOutputError(
	providerID, model, reason string,
	retryable, terminal bool,
	usage TokenUsage,
	maxTokens int,
	entry modelinfo.Entry,
	calls []api.ToolCall,
) error {
	if terminal && failure.IsOutputLimitFinishReason(reason) {
		return NewOutputTruncatedError(providerID, model, reason, usage, maxTokens, entry, calls)
	}
	return &failure.ProviderEmptyCompletionError{
		ProviderID: providerID,
		Model:      model,
		Retryable:  retryable,
		Reason:     reason,
		Terminal:   terminal,
	}
}
