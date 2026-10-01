package anthropic

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

func TestToolUseBlockAlwaysCarriesInput(t *testing.T) {
	for _, block := range []ContentBlock{
		{Type: "tool_use", ID: "t1", Name: "git_status"},
		{Type: "tool_use", ID: "t2", Name: "git_status", Input: map[string]any{}},
	} {
		raw, err := surveyjson.Marshal(block)
		if err != nil {
			t.Fatalf("marshal %s: %v", block.ID, err)
		}
		if !strings.Contains(string(raw), `"input":{}`) {
			t.Fatalf("tool_use %s = %s; the API requires input", block.ID, raw)
		}
	}
	raw, err := surveyjson.Marshal(ContentBlock{Type: "tool_use", ID: "t3", Name: "read", Input: map[string]any{"path": "a<b>.go"}})
	if err != nil {
		t.Fatalf("marshal with input: %v", err)
	}
	if !strings.Contains(string(raw), `"input":{"path":"a<b>.go"}`) || strings.Count(string(raw), `"input"`) != 1 {
		t.Fatalf("tool_use with input = %s", raw)
	}
	raw, err = surveyjson.Marshal(ContentBlock{Type: "text", Text: "hi"})
	if err != nil {
		t.Fatalf("marshal text: %v", err)
	}
	if strings.Contains(string(raw), `"input"`) {
		t.Fatalf("text block = %s; only tool_use carries input", raw)
	}
}
