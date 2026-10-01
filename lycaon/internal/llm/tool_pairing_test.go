package llm

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	anthropicprovider "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	bedrockprovider "github.com/lycaon/lycaon/internal/llm/providers/bedrock"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProviderProjectionPairsDispatchResultsByIdentity(t *testing.T) {
	messages := []api.Message{
		{ID: "wave", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{ID: "backend", WireID: "provider-backend", Name: "task"},
			{ID: "ui", WireID: "provider-ui", Name: "task"},
		}},
		{Role: api.MessageRoleTool, Content: "orphan", ToolResult: &api.ToolResult{ToolCallID: "other"}},
		{Role: api.MessageRoleTool, Content: "invalid UI brief", ToolResult: &api.ToolResult{ToolCallID: "ui", Outcome: api.ToolResultOutcomeRejected}},
		{Role: api.MessageRoleTool, Content: "backend queued", ToolResult: &api.ToolResult{ToolCallID: "backend", Outcome: api.ToolResultOutcomeCompleted}},
		{Role: api.MessageRoleTool, Content: "duplicate", ToolResult: &api.ToolResult{ToolCallID: "backend"}},
	}

	t.Run("OpenAI compatible", func(t *testing.T) {
		wire := openaicompat.ProjectMessages(messages, openaicompat.MessageProjection{RoundTripToolCallIDs: true})
		if len(wire) != 3 || wire[1].ToolCallID != "provider-ui" || wire[2].ToolCallID != "provider-backend" {
			t.Fatalf("dispatch/result pairing changed: %+v", wire)
		}
	})
	t.Run("Anthropic", func(t *testing.T) {
		_, wire := anthropicprovider.ProjectMessages(messages, nil, false, "", "", "")
		if len(wire) != 2 || len(wire[1].Content) != 2 {
			t.Fatalf("unexpected wire messages: %+v", wire)
		}
		blocks := wire[1].Content
		if blocks[0].ToolUseID != "provider-ui" || blocks[1].ToolUseID != "provider-backend" {
			t.Fatalf("incorrect pairing: %+v", blocks)
		}
	})
	t.Run("Bedrock", func(t *testing.T) {
		_, wire := bedrockprovider.ProjectMessages(messages, nil, false, "")
		if len(wire) != 2 || len(wire[1].Content) != 2 {
			t.Fatalf("unexpected wire messages: %+v", wire)
		}
		for i, want := range []string{"provider-ui", "provider-backend"} {
			block, ok := wire[1].Content[i].(*brtypes.ContentBlockMemberToolResult)
			if !ok || aws.ToString(block.Value.ToolUseId) != want {
				t.Fatalf("incorrect result pairing: %+v", wire[1].Content[i])
			}
		}
	})
}
