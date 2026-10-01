package compaction

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestDietOverlayPromoteMessagesElidesProductReadsAndStalePreviews(t *testing.T) {
	messages := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"path": "shellsim/builtins.py"}}}},
		{Role: api.MessageRoleTool, Content: strings.Repeat("x", 600)},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "preview_overlay", Args: map[string]any{"overlay_id": "job-a"}}}},
		{Role: api.MessageRoleTool, Content: `{"job_id":"job-a","mode":"preview"}`},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "preview_overlay", Args: map[string]any{"overlay_id": "job-a", "path": "shellsim/builtins.py"}}}},
		{Role: api.MessageRoleTool, Content: `{"job_id":"job-a","mode":"preview","paths":["shellsim/builtins.py"]}`},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"path": "promote-spills/job-a.json"}}}},
		{Role: api.MessageRoleTool, Content: strings.Repeat("s", 600)},
	}
	out := DietOverlayPromoteMessages(messages)
	if !strings.Contains(out[1].Content, "product read") {
		t.Fatalf("product read not elided: %q", out[1].Content[:80])
	}
	if !strings.Contains(out[3].Content, overlayPromoteDietTombstone) {
		t.Fatalf("stale preview not elided: %q", out[3].Content)
	}
	if out[5].Content == overlayPromoteDietTombstone {
		t.Fatal("latest scoped preview should be kept")
	}
	if strings.Contains(out[7].Content, "product read") {
		t.Fatalf("spill read should not be elided: %q", out[7].Content[:40])
	}
}
