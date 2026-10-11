package session

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReloadHistoryReassemblesDiet(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.HardCeilingTokens = 50000
	cfg.TargetTokens = 8000
	cfg.PruneProtectTailMessages = 4
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	var msgs []api.Message
	big := strings.Repeat("tool-output-line\n", 800)
	for i := 0; i < 24; i++ {
		msgs = append(msgs,
			api.Message{ID: fmt.Sprintf("u%d", i), Role: api.MessageRoleUser, Content: "go"},
			api.Message{
				ID:        fmt.Sprintf("a%d", i),
				Role:      api.MessageRoleAssistant,
				Content:   "working",
				ToolCalls: []api.ToolCall{{ID: fmt.Sprintf("tc%d", i), Name: "read", Args: map[string]any{"path": "x.go"}}},
			},
			api.Message{
				ID:      fmt.Sprintf("t%d", i),
				Role:    api.MessageRoleTool,
				Content: big,
				ToolResult: &api.ToolResult{
					ToolCallID: fmt.Sprintf("tc%d", i),
					Tool:       "read",
					Content:    big,
				},
			},
		)
	}
	testutil.FailErr(t, "append messages", store.AppendMessages(ctx, sess.ID, msgs...))

	assembled, err := mgr.Runner.History.Reload(ctx, sess.ID, sess, "")
	testutil.FailErr(t, "reloadAssembledHistory", err)
	if len(assembled) == 0 {
		t.Fatal("expected assembled projection")
	}
	for _, msg := range assembled {
		if msg.Role != api.MessageRoleTool {
			continue
		}
		if msg.Content != big {
			t.Fatalf("tool %s rewritten: %d bytes want %d", msg.ID, len(msg.Content), len(big))
		}
	}
}

func TestReloadHistoryDoesNotWriteStore(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.HardCeilingTokens = 20000
	cfg.TargetTokens = 4000
	cfg.PruneProtectTailMessages = 2
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	big := strings.Repeat("canonical-tool-body\n", 600)
	testutil.FailErr(t, "append", store.AppendMessages(ctx, sess.ID,
		api.Message{ID: "u1", Role: api.MessageRoleUser, Content: "go"},
		api.Message{
			ID:        "a1",
			Role:      api.MessageRoleAssistant,
			Content:   "ok",
			ToolCalls: []api.ToolCall{{ID: "tc1", Name: "read", Args: map[string]any{"path": "a.go"}}},
		},
		api.Message{
			ID:      "t1",
			Role:    api.MessageRoleTool,
			Content: big,
			ToolResult: &api.ToolResult{
				ToolCallID: "tc1",
				Tool:       "read",
				Content:    big,
			},
		},
		api.Message{ID: "u2", Role: api.MessageRoleUser, Content: "more"},
		api.Message{
			ID:        "a2",
			Role:      api.MessageRoleAssistant,
			Content:   "ok",
			ToolCalls: []api.ToolCall{{ID: "tc2", Name: "read", Args: map[string]any{"path": "b.go"}}},
		},
		api.Message{
			ID:      "t2",
			Role:    api.MessageRoleTool,
			Content: big,
			ToolResult: &api.ToolResult{
				ToolCallID: "tc2",
				Tool:       "read",
				Content:    big,
			},
		},
		api.Message{ID: "u3", Role: api.MessageRoleUser, Content: "again"},
		api.Message{
			ID:        "a3",
			Role:      api.MessageRoleAssistant,
			Content:   "ok",
			ToolCalls: []api.ToolCall{{ID: "tc3", Name: "read", Args: map[string]any{"path": "c.go"}}},
		},
		api.Message{
			ID:      "t3",
			Role:    api.MessageRoleTool,
			Content: big,
			ToolResult: &api.ToolResult{
				ToolCallID: "tc3",
				Tool:       "read",
				Content:    big,
			},
		},
	))

	before, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages before", err)
	beforeLens := make([]int, len(before))
	for i, m := range before {
		beforeLens[i] = len(m.Content)
	}

	assembled, err := mgr.Runner.History.Reload(ctx, sess.ID, sess, "")
	testutil.FailErr(t, "reloadAssembledHistory", err)
	if len(assembled) == 0 {
		t.Fatal("expected assembled projection")
	}

	after, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages after", err)
	if len(after) != len(before) {
		t.Fatalf("store message count changed: before=%d after=%d", len(before), len(after))
	}
	for i := range after {
		if after[i].Content != before[i].Content {
			t.Fatalf("store message %d content mutated after reload assemble", i)
		}
		if len(after[i].Content) != beforeLens[i] {
			t.Fatalf("store message %d length %d want %d", i, len(after[i].Content), beforeLens[i])
		}
	}
}

func TestReloadHistoryFailsClosedWithoutSession(t *testing.T) {
	mgr, _ := newCompactionManager(t, compaction.DefaultCompactionConfig())
	_, err := mgr.Runner.History.Reload(context.Background(), "sess", nil, "")
	if err == nil {
		t.Fatal("expected error when sess is nil")
	}
}
