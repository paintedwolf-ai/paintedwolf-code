package providerwire

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestSystemPreamblePreservesContentOrderAndHostTranscript(t *testing.T) {
	messages := []api.Message{
		{Role: api.MessageRoleSystem, Content: "Application guidance."},
		{Role: api.MessageRoleSystem, Content: "Tool guidance."},
		{Role: api.MessageRoleUser, Content: "Update the report."},
		{Role: api.MessageRoleAssistant, Content: "Report updated."},
		{Role: api.MessageRoleSystem, Content: "Verification failed."},
		{Role: api.MessageRoleAssistant, Content: "I will repair it."},
	}
	original := append([]api.Message(nil), messages...)
	want := []api.Message{
		{Role: api.MessageRoleSystem, Content: "Application guidance.\n\nTool guidance."},
		messages[2], messages[3],
		{Role: api.MessageRoleUser, Content: "Verification failed."},
		messages[5],
	}
	if got := SystemPreamble(messages); !reflect.DeepEqual(got, want) {
		t.Fatalf("projected timeline = %+v", got)
	}
	if !reflect.DeepEqual(messages, original) {
		t.Fatal("wire projection mutated the host transcript")
	}
	withoutPreamble := SystemPreamble(messages[2:])
	if len(withoutPreamble) != 4 || withoutPreamble[2].Role != api.MessageRoleUser {
		t.Fatalf("missing preamble changed the timeline: %+v", withoutPreamble)
	}
}

func TestSystemPreambleEndsAtStandingBoundaryOrHostEvent(t *testing.T) {
	for _, boundary := range []bool{false, true} {
		messages := []api.Message{
			{Role: api.MessageRoleSystem, Content: "standing"},
			{Role: api.MessageRoleSystem, Content: "wake"},
			{Role: api.MessageRoleSystem, Content: "current state"},
		}
		if boundary {
			messages[0].PromptCacheBreakpoint = api.PromptCacheTierStanding
		} else {
			messages[1].Origin = api.MessageOriginHost
			messages[1].Visibility = api.MessageVisibilityInternal
			messages[1].Kind = api.MessageKindHostLoopWake
		}
		original := append([]api.Message(nil), messages...)
		out := SystemPreamble(messages)
		if len(out) != 3 || out[0].Content != "standing" || out[1].Role != api.MessageRoleUser || out[2].Role != api.MessageRoleUser {
			t.Fatalf("host context entered preamble: %+v", out)
		}
		if !reflect.DeepEqual(messages, original) {
			t.Fatal("projection mutated messages")
		}
	}
}

func TestSystemPreambleFoldsPostToolSystemInjectsIntoTrailingToolResult(t *testing.T) {
	toolResult := &api.ToolResult{Tool: "read", ToolCallID: "call-1", Content: "file contents"}
	messages := []api.Message{
		{Role: api.MessageRoleSystem, Content: "standing", PromptCacheBreakpoint: api.PromptCacheTierStanding},
		{Role: api.MessageRoleUser, Content: "Read the file."},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "call-1", Name: "read", Args: map[string]any{"path": "main.go"}}}},
		{Role: api.MessageRoleTool, Content: "file contents", ToolResult: toolResult, PromptCacheBreakpoint: api.PromptCacheTierHistory},
		{Role: api.MessageRoleSystem, Content: "## Workflow"},
		{Role: api.MessageRoleSystem, Content: "## Pack board"},
	}
	out := SystemPreamble(messages)
	if len(out) != 4 {
		t.Fatalf("projected messages = %d, want 4: %+v", len(out), out)
	}
	wantContent := "file contents\n\n## Workflow\n\n## Pack board"
	if out[3].Role != api.MessageRoleTool || out[3].Content != wantContent || out[3].PromptCacheBreakpoint != api.PromptCacheTierHistory {
		t.Fatalf("folded tool message = %+v", out[3])
	}
	if out[3].ToolResult == nil || out[3].ToolResult.Content != wantContent {
		t.Fatalf("folded tool result = %+v", out[3].ToolResult)
	}
	if toolResult.Content != "file contents" || out[3].ToolResult == toolResult {
		t.Fatalf("projection mutated caller ToolResult: %+v", toolResult)
	}
	lastUserIdx := -1
	for i, m := range out {
		if m.Role == api.MessageRoleUser {
			lastUserIdx = i
		}
	}
	if lastUserIdx != 1 {
		t.Fatalf("lastUserIdx = %d, want 1", lastUserIdx)
	}

	// Verify fallback to ToolResult.Content when Message.Content is empty,
	// empty system content handling, and breakpoint forwarding from the system inject.
	fromResultOnly := SystemPreamble([]api.Message{
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "call-2", Content: "raw result"}},
		{Role: api.MessageRoleSystem, Content: ""},
		{Role: api.MessageRoleSystem, Content: "runtime state", PromptCacheBreakpoint: api.PromptCacheTierHistory},
	})
	if len(fromResultOnly) != 1 || fromResultOnly[0].Content != "raw result\n\nruntime state" ||
		fromResultOnly[0].ToolResult.Content != "raw result\n\nruntime state" ||
		fromResultOnly[0].PromptCacheBreakpoint != api.PromptCacheTierHistory {
		t.Fatalf("fallback tool fold = %+v", fromResultOnly)
	}
	emptyTool := SystemPreamble([]api.Message{
		{Role: api.MessageRoleTool},
		{Role: api.MessageRoleSystem, Content: "only system"},
	})
	if len(emptyTool) != 1 || emptyTool[0].Content != "only system" {
		t.Fatalf("empty tool fold = %+v", emptyTool)
	}
}
