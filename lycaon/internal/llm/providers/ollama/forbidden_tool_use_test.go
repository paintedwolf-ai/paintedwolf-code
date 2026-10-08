package ollama

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestForbiddenToolUseOmitsDefinitions(t *testing.T) {
	p := New("desktop", "http://unused", "", []modelinfo.Entry{{ID: "gemma", ContextLength: 131072}})
	req := modelcall.CompletionRequest{
		Model:    "gemma",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		Tools:    []tools.ToolMeta{{Name: "read", ArgsSchema: map[string]any{"type": "object"}}},
	}
	built, _, err := p.Prepare(t.Context(), req, false)
	testutil.FailErr(t, "prepare allowed", err)
	if len(built.Tools) != 1 {
		t.Fatalf("allowed tool use dropped tools: %+v", built.Tools)
	}
	req.ToolUse = modelcall.ToolUseForbidden
	built, _, err = p.Prepare(t.Context(), req, false)
	testutil.FailErr(t, "prepare forbidden", err)
	if len(built.Tools) != 0 {
		t.Fatalf("forbidden tool use still offered %d tools", len(built.Tools))
	}
}
