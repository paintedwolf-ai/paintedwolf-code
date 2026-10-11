package toolpresentation

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestPlanToolStashReplayInjectsBlock(t *testing.T) {
	stash := NewStash()
	block := ">>> Spec posture blocked\nCode: SPEC_POSTURE_STUB_REQUIRED"
	stash.Put("sess-1", "call-1", "SPEC_POSTURE_STUB_REQUIRED", block)

	history := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "call-1", Name: "write"}}},
		{Role: api.MessageRoleTool, Content: ""},
	}
	out := EnrichHistory("sess-1", history, stash)
	if !strings.Contains(out[1].Content, "SPEC_POSTURE_STUB_REQUIRED") {
		t.Fatalf("expected replayed block, got %q", out[1].Content)
	}
}
