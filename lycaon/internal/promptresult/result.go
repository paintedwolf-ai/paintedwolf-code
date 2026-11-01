// Package promptresult names the outcome of running one prompt turn.
package promptresult

// Outcome classifies how a prompt turn finished.
type Outcome string

const (
	// OutcomeCompleted is the default normal turn outcome.
	OutcomeCompleted Outcome = ""
	// OutcomeOutputTruncated marks a turn that ended cleanly after a recovery
	// attempt also stopped on an output token limit with no usable content.
	OutcomeOutputTruncated Outcome = "output_truncated"
)

// OutputTruncatedOutcome carries structured details when a prompt turn ends on
// an output token limit after recovery.
type OutputTruncatedOutcome struct {
	ProviderID          string `json:"provider_id,omitempty"`
	Model               string `json:"model,omitempty"`
	FinishReason        string `json:"finish_reason,omitempty"`
	MaxTokens           int    `json:"max_tokens,omitempty"`
	ConfiguredMaxTokens int    `json:"configured_max_tokens,omitempty"`
	ProviderMaxTokens   int    `json:"provider_max_tokens,omitempty"`
	LowOutputCeiling    bool   `json:"low_output_ceiling,omitempty"`
	ToolCallTruncated   bool   `json:"tool_call_truncated,omitempty"`
	ToolName            string `json:"tool_name,omitempty"`
	ToolArgsBytes       int    `json:"tool_args_bytes,omitempty"`
	Notice              string `json:"notice,omitempty"`
}

// Result identifies the assistant message a prompt turn produced.
type Result struct {
	MessageID string
	Outcome   Outcome `json:"outcome,omitempty"`
	// OutputTruncated describes the output-limit termination when Outcome is
	// OutcomeOutputTruncated.
	OutputTruncated *OutputTruncatedOutcome `json:"output_truncated,omitempty"`
	// OfferedTools are the tools the final model request could call. They are
	// not persisted: a replayed or recovered result carries none.
	OfferedTools []string `json:"-"`
}

// OfferedToolNames is nil-safe: a missing result offers no tools.
func (r *Result) OfferedToolNames() []string {
	if r == nil {
		return nil
	}
	return r.OfferedTools
}
