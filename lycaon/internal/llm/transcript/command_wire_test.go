package transcript_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	anthropic "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	bedrock "github.com/lycaon/lycaon/internal/llm/providers/bedrock"
	"github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	vertex "github.com/lycaon/lycaon/internal/llm/providers/vertexexpress"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func recoveryWireMessage(t *testing.T) api.Message {
	t.Helper()
	status := 2
	raw, err := json.Marshal(map[string]any{"program_id": "program", "state": "rejected", "status": status, "stdout": "", "stderr": "earlier output\n", "refusal": map[string]any{"code": "COMMAND_SYNTAX_INVALID", "feedback": "Rejected: shell syntax error\nFix: close the string\nCode: COMMAND_SYNTAX_INVALID"}})
	testutil.FailErr(t, "encode recovery result", err)
	return api.Message{ID: "tool-result", Role: api.MessageRoleTool, Origin: api.MessageOriginTool, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted, Content: string(raw), ToolResult: &api.ToolResult{Tool: "command", ToolCallID: "call", Content: string(raw), Invocation: &api.InvocationReceipt{ID: "invocation", Tool: "command", ToolCallID: "call", Status: "completed", Invoked: true}, CommandProgram: &api.CommandProgramReceipt{ID: "program", ParentReceiptID: "invocation", State: "rejected", StatusCode: &status, StopCode: "COMMAND_SYNTAX_INVALID"}}}
}

func TestCommandRecoverySurvivesCompactionRoundTrips(t *testing.T) {
	for _, projectFirst := range []bool{false, true} {
		messages := []api.Message{recoveryWireMessage(t)}
		if projectFirst {
			messages = transcript.Project(messages)
		}
		for _, original := range [][]api.Message{nil, messages} {
			round := compaction.ContextMessagesToAPI(compaction.ContextMessagesFromAPI(messages), original)
			projected := transcript.Project(round)
			got := projected[len(projected)-1].Content
			if !strings.Contains(got, "Command refusal:") || !strings.Contains(got, "\n⟦D⟧Fix: close the string") || strings.Contains(got, `"feedback"`) {
				t.Fatalf("recovery became escaped or lost: projectFirst=%t, %s", projectFirst, got)
			}
		}
	}
}

func TestCommandRecoveryHasReadableProviderWire(t *testing.T) {
	messages := transcript.Project([]api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "call", Name: "command", Args: map[string]any{"command": "eval 'if'"}}}},
		recoveryWireMessage(t),
	})
	for _, provider := range []struct {
		name    string
		project func([]api.Message) any
	}{
		{"openai", func(m []api.Message) any { return openaicompat.ProjectMessages(m, openaicompat.MessageProjection{}) }},
		{"ollama", func(m []api.Message) any { return ollama.ProjectMessages(m, ollama.MessageProjection{}) }},
		{"anthropic", func(m []api.Message) any { _, out := anthropic.ProjectMessages(m, nil, false, "", "", ""); return out }},
		{"bedrock", func(m []api.Message) any { _, out := bedrock.ProjectMessages(m, nil, false, ""); return out }},
		{"vertex", func(m []api.Message) any { _, out := vertex.ProjectMessages(m, false, ""); return out }},
	} {
		t.Run(provider.name, func(t *testing.T) {
			raw, err := json.Marshal(provider.project(messages))
			testutil.FailErr(t, "encode provider wire", err)
			var wire any
			testutil.FailErr(t, "decode provider wire", json.Unmarshal(raw, &wire))
			if !wireContainsReadableRecovery(wire) {
				t.Fatalf("provider lost readable refusal: %s", raw)
			}
		})
	}
}

func wireContainsReadableRecovery(value any) bool {
	switch v := value.(type) {
	case string:
		return strings.Contains(v, "Command refusal:") && strings.Contains(v, "\n⟦D⟧Fix: close the string")
	case []any:
		for _, child := range v {
			if wireContainsReadableRecovery(child) {
				return true
			}
		}
	case map[string]any:
		for _, child := range v {
			if wireContainsReadableRecovery(child) {
				return true
			}
		}
	}
	return false
}
