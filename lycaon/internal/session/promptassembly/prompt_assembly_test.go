package promptassembly_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/promptassembly"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAssemblePromptHistory_idempotent(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.HardCeilingTokens = 8000
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "hello"},
		{Role: api.MessageRoleAssistant, Content: "world"},
	}
	deps := promptassembly.Config{CompactionConfig: cfg}
	once, _ := promptassembly.Assemble(nil, msgs, deps)
	twice, _ := promptassembly.Assemble(nil, once, deps)
	if len(once) != len(twice) {
		t.Fatalf("len once=%d twice=%d", len(once), len(twice))
	}
	for i := range once {
		if once[i].Content != twice[i].Content {
			t.Fatalf("message %d changed on second assembly", i)
		}
	}
}

func TestAssemblePromptHistory_overlayPromoteSurfaceDiet(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "go"},
		{Role: api.MessageRoleTool, Content: "product read body", ToolResult: &api.ToolResult{Tool: "read"}},
	}
	deps := promptassembly.Config{
		SurfaceID:        surface.SurfaceImplementOverlayPromote,
		CompactionConfig: compaction.DefaultCompactionConfig(),
	}
	out, report := promptassembly.Assemble(nil, msgs, deps)
	if len(out) == len(msgs) && out[1].Content == msgs[1].Content {
		// When the diet keeps the product read, the surface strategy is still recorded.
		if !containsStrategy(report.Strategies, "surface:overlay_promote") {
			t.Fatalf("strategies=%v", report.Strategies)
		}
	}
}

func TestAssemblePromptHistory_sealsToolBodies(t *testing.T) {
	body := strings.Repeat("summarize-pack-body-", 400)
	msgs := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "go"},
		{ID: "a1", Role: api.MessageRoleAssistant, Content: "ok"},
		{ID: "t1", Role: api.MessageRoleTool, Content: body, ToolResult: &api.ToolResult{Tool: "summarize", Content: body}},
	}
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.HardCeilingTokens = 8000
	out, report := promptassembly.Assemble(nil, msgs, promptassembly.Config{
		CompactionConfig: cfg,
	})
	if !containsStrategy(report.Strategies, "seal:tool_bodies") {
		t.Fatalf("strategies=%v want seal:tool_bodies", report.Strategies)
	}
	found := false
	for _, msg := range out {
		if msg.ID != "t1" {
			continue
		}
		found = true
		if msg.Content != body {
			t.Fatalf("tool body rewritten: got %d bytes want %d", len(msg.Content), len(body))
		}
	}
	if !found {
		t.Fatal("sealed tool row missing after assembly")
	}
}

func containsStrategy(strategies []string, want string) bool {
	for _, s := range strategies {
		if s == want {
			return true
		}
	}
	return false
}
