package nextstepreport

import (
	"testing"

	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/pkg/api"
)

func toolResult(id, tool string, outcome api.ToolResultOutcome, feedback ...api.ToolFeedback) api.Message {
	return api.Message{ID: id, Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: tool, Outcome: outcome, Feedback: feedback}}
}

func offer(code string, tools ...string) api.ToolFeedback {
	calls := make([]toolreach.Call, 0, len(tools))
	for _, tool := range tools {
		calls = append(calls, toolreach.Call{Tool: tool})
	}
	return api.ToolFeedback{Code: code, Details: map[string]any{api.ToolFeedbackNextCalls: calls}}
}

func TestReportFindsAnOfferedCallTheHostRefused(t *testing.T) {
	var report Report
	report.Add([]api.Message{
		toolResult("m1", "read", api.ToolResultOutcomeRejected, offer("READ_IS_DIRECTORY", "command", "recall")),
		toolResult("m2", "command", api.ToolResultOutcomeRejected, api.ToolFeedback{Code: "PATH_DENIED"}),
		toolResult("m3", "other", api.ToolResultOutcomeCompleted),
	})
	if report.Offered != 2 || report.Followed != 1 || report.ByCode["READ_IS_DIRECTORY"] != 2 {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Refused) != 1 || report.Refused[0] != (Refusal{OfferedBy: "READ_IS_DIRECTORY", Tool: "command", Code: "PATH_DENIED", MessageID: "m2"}) {
		t.Fatalf("refused = %+v", report.Refused)
	}
}

func TestReportLeavesAFollowedCallThatRan(t *testing.T) {
	var report Report
	report.Add([]api.Message{
		toolResult("m1", "read", api.ToolResultOutcomeRejected, offer("READ_IS_DIRECTORY", "command")),
		toolResult("m2", "command", api.ToolResultOutcomeCompleted),
	})
	if report.Followed != 1 || len(report.Refused) != 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestReportDoesNotCountACallOutsideTheWindow(t *testing.T) {
	var report Report
	messages := []api.Message{toolResult("m0", "read", api.ToolResultOutcomeRejected, offer("X", "command"))}
	for range followWindow {
		messages = append(messages, toolResult("mid", "other", api.ToolResultOutcomeCompleted))
	}
	report.Add(append(messages, toolResult("late", "command", api.ToolResultOutcomeRejected)))
	if report.Followed != 0 || len(report.Refused) != 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestReportMatchesOfferedProgramInsteadOfUnrelatedCommand(t *testing.T) {
	var report Report
	offeredLs := api.ToolFeedback{
		Code:    "READ_IS_DIRECTORY",
		Details: map[string]any{api.ToolFeedbackNextCalls: []toolreach.Call{toolreach.ProgramCall("ls", "src")}},
	}
	unrelated := toolResult("m2", "command", api.ToolResultOutcomeRejected, api.ToolFeedback{Code: "PATH_DENIED"})
	unrelated.ToolResult.ToolArgs = map[string]any{"command": "cat secret.txt"}
	followed := toolResult("m3", "command", api.ToolResultOutcomeCompleted)
	followed.ToolResult.ToolArgs = map[string]any{"command": "ls src"}
	report.Add([]api.Message{
		toolResult("m1", "read", api.ToolResultOutcomeRejected, offeredLs),
		unrelated,
		followed,
	})
	if report.Followed != 1 || len(report.Refused) != 0 {
		t.Fatalf("report = %+v", report)
	}
}
