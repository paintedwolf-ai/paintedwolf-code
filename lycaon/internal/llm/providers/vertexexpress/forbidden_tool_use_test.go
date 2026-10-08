package vertexexpress

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestForbiddenToolUseKeepsDeclarationsAndDisablesCalling(t *testing.T) {
	p := vertexExpressTestProvider()
	req := modelcall.CompletionRequest{
		Model:    "gemini-2.5-flash",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		Tools:    []tools.ToolMeta{{Name: "read", ArgsSchema: map[string]any{"type": "object"}}},
	}
	if got := p.Prepare(req); got.ToolConfig != nil {
		t.Fatalf("allowed tool use sent toolConfig %+v", got.ToolConfig)
	}
	req.ToolUse = modelcall.ToolUseForbidden
	got := p.Prepare(req)
	if got.ToolConfig == nil || got.ToolConfig.FunctionCallingConfig.Mode != "NONE" || len(got.Tools) != 1 {
		t.Fatalf("toolConfig = %+v tools = %d, want mode NONE with declarations kept", got.ToolConfig, len(got.Tools))
	}
}
