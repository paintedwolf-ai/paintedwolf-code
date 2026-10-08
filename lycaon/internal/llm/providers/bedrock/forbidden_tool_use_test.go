package bedrock

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// Converse has no no-call choice and validates tool history against the tool
// configuration, so a forbidding request keeps its definitions.
func TestForbiddenToolUseKeepsToolConfiguration(t *testing.T) {
	p := New("bedrock", "us-east-1", "", nil)
	req := modelcall.CompletionRequest{
		Model:    "anthropic.claude",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		Tools:    []tools.ToolMeta{{Name: "read", ArgsSchema: map[string]any{"type": "object"}}},
		ToolUse:  modelcall.ToolUseForbidden,
	}
	input := p.buildConverseInput(req, "anthropic.claude")
	if input.ToolConfig == nil || len(input.ToolConfig.Tools) != 1 || input.ToolConfig.ToolChoice != nil {
		t.Fatalf("tool config = %+v, want the definitions and no choice", input.ToolConfig)
	}
}
