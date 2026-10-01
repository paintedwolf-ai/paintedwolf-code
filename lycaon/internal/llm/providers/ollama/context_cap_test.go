package ollama

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOllamaEstimatesToolContentOnce(t *testing.T) {
	content := strings.Repeat("result ", 100)
	for _, message := range []api.Message{
		{Role: api.MessageRoleTool, Content: content},
		{Role: api.MessageRoleTool, Content: content, ToolResult: &api.ToolResult{Content: content}},
		{Role: api.MessageRoleTool, Content: content, ToolResult: &api.ToolResult{Content: "stored original"}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Content: content}},
	} {
		wire := ProjectMessages([]api.Message{message}, false, "")
		want := estimatePromptTokens(modelcall.CompletionRequest{Messages: []api.Message{
			{Role: api.MessageRoleTool, Content: wire[0].Content},
		}})
		if got := estimatePromptTokens(modelcall.CompletionRequest{Messages: []api.Message{message}}); got != want {
			t.Fatalf("tool estimate = %d, want wire content estimate %d", got, want)
		}
	}
}

func TestOllamaPromptCalibrationReplacesColdStartPadding(t *testing.T) {
	p := New("remote", "http://unused.invalid", "", nil)
	if got := p.projectPromptTokens("model", 100); got != 150 {
		t.Fatalf("cold projection = %d, want 150", got)
	}
	p.observePromptTokens("model", 100, 100)
	if got := p.projectPromptTokens("model", 100); got != 110 {
		t.Fatalf("calibrated projection = %d, want 110", got)
	}
	p.observePromptTokens("model", 100, 50)
	if got := p.projectPromptTokens("model", 100); got != 110 {
		t.Fatalf("terse prompt reduced learned headroom to %d", got)
	}
	p.observePromptTokens("model", 100, 200)
	if got := p.projectPromptTokens("model", 100); got != 220 {
		t.Fatalf("expanded tokenizer projection = %d, want 220", got)
	}
	p.observePromptTokens("terse", 100, 10)
	if got := p.projectPromptTokens("terse", 100); got != 100 {
		t.Fatalf("projection = %d, must not undercut the raw estimate", got)
	}
	if got := p.projectPromptTokens("unseen", 100); got != 150 {
		t.Fatalf("unseen model projection = %d, want cold-start padding", got)
	}
}
