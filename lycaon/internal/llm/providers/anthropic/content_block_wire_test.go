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

func TestThinkingBlockAlwaysCarriesThinkingField(t *testing.T) {
	for _, block := range []ContentBlock{
		{Type: "thinking", Thinking: "", Signature: "sig-empty"},
		{Type: "thinking", Thinking: "summary", Signature: "sig-text"},
	} {
		raw, err := surveyjson.Marshal(block)
		if err != nil {
			t.Fatalf("marshal %s: %v", block.Signature, err)
		}
		if !strings.Contains(string(raw), `"thinking":`) {
			t.Fatalf("thinking %s = %s; the API requires thinking", block.Signature, raw)
		}
	}
	for _, block := range []ContentBlock{
		{Type: "text", Text: "hi"},
		{Type: "tool_use", ID: "t1", Name: "git_status"},
		{Type: "tool_result", ToolUseID: "t1", ResultText: "ok"},
		{Type: "redacted_thinking", Data: "secret"},
		{Type: "image", Source: &ImageSource{Type: "base64", MediaType: "image/png", Data: "YWJj"}},
	} {
		raw, err := surveyjson.Marshal(block)
		if err != nil {
			t.Fatalf("marshal %s: %v", block.Type, err)
		}
		if strings.Contains(string(raw), `"thinking"`) {
			t.Fatalf("%s block = %s; only thinking carries thinking", block.Type, raw)
		}
	}
}
