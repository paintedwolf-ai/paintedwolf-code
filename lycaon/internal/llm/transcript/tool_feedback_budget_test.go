package transcript

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFirstFeedbackBudgetMatchesProjectionEnvelope(t *testing.T) {
	feedback := api.ToolFeedback{Code: "TOOL_NOT_OFFERED", Details: map[string]any{"data": ""}}
	encoded, err := json.Marshal(feedback)
	testutil.FailErr(t, "encode fixture", err)
	remaining := maxToolFeedbackBytes - toolFeedbackEnvelopeReserve - maxInvocationFeedbackBytes - len(encoded) - 1
	feedback.Details["data"] = strings.Repeat("x", remaining)
	if !ToolFeedbackFitsFirst(feedback) {
		t.Fatal("exact bounded feedback refused")
	}
	result := &api.ToolResult{Tool: "unknown", ToolCallID: "call", Feedback: []api.ToolFeedback{feedback}, Invocation: &api.InvocationReceipt{ID: "invocation", Tool: "unknown", ToolCallID: "call"}}
	// Maximize the optional invocation without changing its pairing.
	result.Invocation.SourceRootDigest = strings.Repeat("s", maxInvocationFeedbackBytes)
	for {
		raw, omitted := invocationFeedback(result)
		if !omitted && len(raw) > 0 {
			break
		}
		result.Invocation.SourceRootDigest = result.Invocation.SourceRootDigest[1:]
	}
	parts := toolFeedbackParts(api.Message{Role: api.MessageRoleTool, ToolResult: result})
	if len(parts) != 1 || strings.Contains(parts[0].Content, "details_omitted") || len(parts[0].Content) > maxToolFeedbackBytes {
		t.Fatal("predicate disagreed with actual first-entry projection")
	}
	feedback.Details["data"] = strings.Repeat("x", remaining+1)
	if ToolFeedbackFitsFirst(feedback) {
		t.Fatal("oversized feedback promised delivery")
	}
	feedback.Details["data"] = make(chan int)
	if ToolFeedbackFitsFirst(feedback) {
		t.Fatal("unserializable feedback promised delivery")
	}
}
