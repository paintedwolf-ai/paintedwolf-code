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

func TestAssemblePromptHistoryOverlayReviewKeepsSourceEvidence(t *testing.T) {
	messages := []api.Message{{ID: "user", Role: api.MessageRoleUser, Content: "Finish the returned worker change."}}
	for _, call := range []api.ToolCall{
		{ID: "read-before", Name: "read", Args: map[string]any{"path": "README.md"}},
		{ID: "preview", Name: "preview_overlay", Args: map[string]any{"overlay_id": "worker"}},
		{ID: "read-after", Name: "read", Args: map[string]any{"path": "test_status.py"}},
	} {
		messages = append(messages,
			api.Message{ID: "assistant-" + call.ID, Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{call}},
			api.Message{ID: call.ID, Role: api.MessageRoleTool, Content: "evidence-" + call.ID,
				ToolResult: &api.ToolResult{Tool: call.Name, ToolCallID: call.ID, Content: "evidence-" + call.ID}},
		)
	}
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled, cfg.HardCeilingTokens = true, 8000
	out, _ := promptassembly.Assemble(nil, messages, promptassembly.Config{
		SurfaceID: surface.SurfaceImplementOverlayPromote, CompactionConfig: cfg,
	})
	if len(out) != len(messages) {
		t.Fatalf("small review exchange lost rows: got %d want %d", len(out), len(messages))
	}
	for i, msg := range out {
		if msg.Content != messages[i].Content {
			t.Fatalf("review evidence %s rewritten: %q", msg.ID, msg.Content)
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

func TestAssemblePromptHistoryPreservesRecoveryOrderThroughContextRoundtrip(t *testing.T) {
	original := []api.Message{
		{ID: "draft", Role: api.MessageRoleAssistant, Content: "done", DraftStatus: api.DraftStatusCommitted},
		{ID: "recovery", Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem,
			Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindCoordinatorGuidance,
			HostSignalID: api.DraftRecoverySignal("draft", "guidance:RETRY"), Content: "take the pending action"},
	}
	view := compaction.ContextMessagesToAPI(compaction.ContextMessagesFromAPI(original), original)
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.HardCeilingTokens = 8000
	got, _ := promptassembly.Assemble(nil, view, promptassembly.Config{CompactionConfig: cfg})
	if len(got) != 2 || got[0].ID != "recovery" || got[1].ID != "draft" {
		t.Fatalf("recovery order lost in fitted history: %+v", got)
	}
	if got[0].HostSignalID != original[1].HostSignalID {
		t.Fatal("context roundtrip discarded recovery identity")
	}
}
