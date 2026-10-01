package ollama

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestOllamaReservesReasoningAndOutputWithinContext(t *testing.T) {
	p := New("local-fixture", "http://unused", "", []modelinfo.Entry{{ID: "fixture", ContextLength: 8192, ThinkStyle: "boolean_think"}})
	req := modelcall.CompletionRequest{Model: "fixture", Tools: []tools.ToolMeta{{Name: "read"}}, AttemptBudget: &modelcall.CompletionBudget{}}
	out, prompt, err := p.Prepare(t.Context(), req, false)
	if err != nil {
		t.Fatalf("build small-context reasoning request: %v", err)
	}
	if out.Options.NumPredict <= 0 || prompt+out.Options.NumPredict+ollamaContextHeadroom > out.Options.NumCtx || req.AttemptBudget.MaxTokens != out.Options.NumPredict {
		t.Fatalf("unreserved reasoning/output room: prompt=%d options=%+v budget=%+v", prompt, out.Options, req.AttemptBudget)
	}
}
