package ollama

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestOllamaDriverOmitsThinkForAlwaysOnModel(t *testing.T) {
	withThinkingRules(t, []modelinfo.ThinkingRule{
		{Match: []string{"locked-reasoner"}, Style: "boolean_think", AlwaysOn: true},
	})
	p := New("ollama", "http://localhost:11434/v1", "", nil)
	req := modelcall.CompletionRequest{Model: "locked-reasoner:8b", Think: modelcall.ThinkOff, Tools: []tools.ToolMeta{{Name: "read"}}}
	if think := p.resolveThink(req, req.Model); think != nil {
		t.Fatalf("think = %v want omitted for always-on model", think)
	}
}
