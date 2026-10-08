package anthropic

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestForbiddenToolUseKeepsDefinitionsAndSendsNoCallChoice(t *testing.T) {
	p := New("anthropic", "https://api.anthropic.com/v1", "k", []modelinfo.Entry{{ID: "claude"}})
	req := modelcall.CompletionRequest{
		Model:    "claude",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		Tools:    []tools.ToolMeta{{Name: "read", ArgsSchema: map[string]any{"type": "object"}}},
	}
	if got := p.Prepare(req, false); got.ToolChoice != nil {
		t.Fatalf("allowed tool use sent tool_choice %+v", got.ToolChoice)
	}
	req.ToolUse = modelcall.ToolUseForbidden
	got := p.Prepare(req, false)
	if got.ToolChoice == nil || got.ToolChoice.Type != "none" || len(got.Tools) != 1 {
		t.Fatalf("tool_choice = %+v tools = %d, want none with definitions kept", got.ToolChoice, len(got.Tools))
	}
}
