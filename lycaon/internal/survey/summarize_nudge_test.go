package survey

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestStackedSurveyToolsSinceBoundary(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "how do plugins work"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read"}, {Name: "grep"}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted}},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "find"}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted}},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "list_dir"}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted}},
	}
	since := api.UserIntentBoundary(history)
	if got := StackedSurveyTools(history, since); got != 3 {
		t.Fatalf("StackedSurveyTools = %d want 3 (read+grep+find, list_dir excluded)", got)
	}
}

func TestSummarizeSinceBoundary(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "explain plugins"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read"}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted}},
	}
	since := api.UserIntentBoundary(history)
	if SummarizeSinceBoundary(history, since) {
		t.Fatal("expected no summarize yet")
	}
	history = append(history,
		api.Message{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "summarize"}}},
		api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted}},
	)
	if !SummarizeSinceBoundary(history, since) {
		t.Fatal("expected summarize since boundary")
	}
}

func TestTaskSinceBoundary(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "fix bug"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "task"}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted}},
	}
	since := api.UserIntentBoundary(history)
	if !TaskSinceBoundary(history, since) {
		t.Fatal("expected task since boundary")
	}
}
