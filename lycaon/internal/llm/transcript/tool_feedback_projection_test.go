package transcript

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func feedbackMessage() api.Message {
	return api.Message{Role: api.MessageRoleTool, Content: "Use the native replacement.", ToolResult: &api.ToolResult{
		Tool: "command", ToolCallID: "call", Outcome: api.ToolResultOutcomeRejected,
		Feedback: []api.ToolFeedback{{Code: "USE_NATIVE_TOOL", Details: map[string]any{
			"replacement_calls": []any{map[string]any{"tool": "git_log", "args": map[string]any{"ref": "benchmark-page", "limit": 6}}},
		}}},
	}}
}

func TestToolFeedbackDeliversExactReplacementAsData(t *testing.T) {
	msg := feedbackMessage()
	parts := toolFeedbackParts(msg)
	if len(parts) != 1 || parts[0].Authority != api.ContentAuthorityNone || parts[0].Origin != api.MessageOriginHost {
		t.Fatalf("feedback must be host data: %+v", parts)
	}
	var payload struct {
		Feedback []api.ToolFeedback `json:"tool_feedback"`
	}
	testutil.FailErr(t, "decode feedback", json.Unmarshal([]byte(parts[0].Content), &payload))
	calls := payload.Feedback[0].Details["replacement_calls"].([]any)
	call := calls[0].(map[string]any)
	args := call["args"].(map[string]any)
	if call["tool"] != "git_log" || args["ref"] != "benchmark-page" || args["limit"] != float64(6) {
		t.Fatalf("replacement changed: %+v", call)
	}
	projected := projectOne(t, msg)
	if !strings.Contains(projected.Content, "⟦D⟧"+parts[0].Content) || projectOne(t, projected).Content != projected.Content {
		t.Fatal("feedback missing or duplicated on model projection")
	}
	msg.Role = api.MessageRoleAssistant
	if len(toolFeedbackParts(msg)) != 0 {
		t.Fatal("assistant content projected as tool feedback")
	}
}

func TestToolFeedbackBoundsWholeRecords(t *testing.T) {
	msg := feedbackMessage()
	msg.ToolResult.Feedback[0].Details = map[string]any{"large": strings.Repeat("x", maxToolFeedbackBytes)}
	msg.ToolResult.Feedback = append(msg.ToolResult.Feedback, api.ToolFeedback{Code: "GIT_OPERATION_PRECONDITION", Details: map[string]any{"reason": "branch_exists"}})
	parts := toolFeedbackParts(msg)
	if len(parts) != 1 || len(parts[0].Content) > maxToolFeedbackBytes || !json.Valid([]byte(parts[0].Content)) || !strings.Contains(parts[0].Content, `"details_omitted":true`) || !strings.Contains(parts[0].Content, "branch_exists") {
		t.Fatalf("feedback bound lost useful records: %+v", parts)
	}
}

func TestOversizedVerdictFeedbackRetainsRepairIssues(t *testing.T) {
	msg := feedbackMessage()
	msg.ID = "durable-result"
	msg.ToolResult.Tool = "submit_verdict"
	issues := make([]any, 20)
	for i := range issues {
		issues[i] = map[string]any{"kind": "missing_assessment", "fact_id": "execute/leg-1"}
	}
	msg.ToolResult.Feedback[0].Details = map[string]any{"expected_call": strings.Repeat("x", maxToolFeedbackBytes), "issues": issues}
	parts := toolFeedbackParts(msg)
	var payload struct {
		Feedback []struct {
			Details         map[string]any `json:"details"`
			ResultMessageID string         `json:"result_message_id"`
		} `json:"tool_feedback"`
	}
	testutil.FailErr(t, "decode projected diagnostics", json.Unmarshal([]byte(parts[0].Content), &payload))
	entry := payload.Feedback[0]
	if len(entry.Details["issues"].([]any)) != 8 || entry.Details["issues_omitted"] != float64(12) || entry.ResultMessageID != msg.ID {
		t.Fatalf("repair issues or durable reference lost: %+v", entry)
	}
	if len(parts[0].Content) > maxToolFeedbackBytes || len(msg.ToolResult.Feedback[0].Details["issues"].([]any)) != 20 {
		t.Fatal("projection changed durable diagnostics or exceeded budget")
	}
}
