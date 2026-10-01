package promptassembly_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/promptassembly"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

func largeSummarizePackContent(t *testing.T) string {
	t.Helper()
	substance := make([]map[string]any, 0, 16)
	for i := 0; i < 16; i++ {
		substance = append(substance, map[string]any{
			"path":       "pkg/plugin.go",
			"start_line": i*20 + 1,
			"end_line":   i*20 + 20,
			"symbol":     "Fn",
			"body":       strings.Repeat("x", 900),
		})
	}
	payload := map[string]any{
		"task": "how to make a plugin",
		"pack": map[string]any{
			"identity": []map[string]any{
				{"path": "pkg/plugin.go", "kind": "file", "line_count": 400, "parse_health": "ok"},
			},
			"skeleton": []map[string]any{
				{"path": "pkg/plugin.go", "kind": "function", "name": "define", "line": 10},
			},
			"substance": substance,
			"imports": []map[string]any{
				{"from": "pkg/plugin.go", "to": "effect", "kind": "import"},
			},
		},
		"anchors": []map[string]any{
			{"handle": "summarize#1", "path": "pkg/plugin.go", "line": 10, "excerpt": "export function define"},
		},
	}
	raw, err := json.Marshal(payload)
	testutil.FailErr(t, "json.Marshal failed", err)
	return string(raw)
}

func TestAssembleSealsAgedSummarizePacks(t *testing.T) {
	pack := largeSummarizePackContent(t)
	rawTok := tokenest.EstimateDefault(pack)
	if rawTok < 2000 {
		t.Fatalf("fixture too small: %d tokens", rawTok)
	}
	msgs := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "survey plugins"},
		{ID: "a1", Role: api.MessageRoleAssistant, Content: "summarizing", ToolCalls: []api.ToolCall{{ID: "tc1", Name: "summarize"}}},
		{
			ID:      "t1",
			Role:    api.MessageRoleTool,
			Content: pack,
			ToolResult: &api.ToolResult{
				ToolCallID: "tc1",
				Tool:       "summarize",
				Content:    pack,
			},
		},
		{ID: "a2", Role: api.MessageRoleAssistant, Content: "next steps after pack"},
		{ID: "u2", Role: api.MessageRoleUser, Content: "fresh turn"},
		{ID: "a3", Role: api.MessageRoleAssistant, Content: "summarize again", ToolCalls: []api.ToolCall{{ID: "tc2", Name: "summarize"}}},
		{
			ID:      "t2",
			Role:    api.MessageRoleTool,
			Content: pack,
			ToolResult: &api.ToolResult{
				ToolCallID: "tc2",
				Tool:       "summarize",
				Content:    pack,
			},
		},
	}
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	out, report := promptassembly.Assemble(nil, msgs, promptassembly.Config{
		CompactionConfig: cfg,
	})
	if !containsStrategy(report.Strategies, "seal:tool_bodies") {
		t.Fatalf("strategies missing seal:tool_bodies: %v", report.Strategies)
	}
	if containsStrategy(report.Strategies, "diet:aged_summarize") {
		t.Fatalf("assembly must not rewrite summarize packs: %v", report.Strategies)
	}
	for _, msg := range out {
		if msg.Role != api.MessageRoleTool {
			continue
		}
		if msg.Content != pack {
			t.Fatalf("tool %s rewritten: %d bytes want %d", msg.ID, len(msg.Content), len(pack))
		}
	}
}

func TestAssembleDoesNotMutateStoreSummarize(t *testing.T) {
	pack := largeSummarizePackContent(t)
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "survey"},
		{Role: api.MessageRoleAssistant, Content: "go", ToolCalls: []api.ToolCall{{ID: "tc1", Name: "summarize"}}},
		{
			ID:      "sum1",
			Role:    api.MessageRoleTool,
			Content: pack,
			ToolResult: &api.ToolResult{
				ToolCallID: "tc1",
				Tool:       "summarize",
				Content:    pack,
			},
		},
		{Role: api.MessageRoleAssistant, Content: "done"},
	}
	original := msgs[2].Content
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	_, _ = promptassembly.Assemble(nil, msgs, promptassembly.Config{
		CompactionConfig: cfg,
	})
	if msgs[2].Content != original {
		t.Fatal("AssemblePromptHistory must not mutate caller/store summarize content")
	}
	if msgs[2].ToolResult != nil && msgs[2].ToolResult.Content != pack {
		t.Fatal("AssemblePromptHistory must not mutate ToolResult.Content")
	}
}
