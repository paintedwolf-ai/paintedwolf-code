//go:build integration

package session_test

import (
	"testing"

	"github.com/google/uuid"
	coordinatorsurface "github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMockLLMRecoversPersistedToolSchemas(t *testing.T) {
	recorder := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ready"}},
	}))
	fixture := setupContextualToolsFixtureWithLLM(t, api.SessionPostureBuild, recorder)
	eligible := true
	fixture.Mgr.SetCoordinatorTurnFrameSource(stubCoordinatorContext{ctx: api.CoordinatorRunContext{
		WorkflowID:                   "implement",
		CurrentPhase:                 "work",
		WorkflowDefaultExecutionMode: coordinatorsurface.ExecutionModeFamilyInvestigate,
		WorkflowInvestigateEligible:  &eligible,
	}})
	fixture.Mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	assistantID, callID := uuid.NewString(), uuid.NewString()
	testutil.FailErr(t, "persist schema activation", fixture.Store.AppendMessages(t.Context(), fixture.Sess.ID,
		api.Message{ID: assistantID, Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: callID, Name: "request_tools"}}},
		api.Message{ID: uuid.NewString(), Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Tool: "request_tools", ToolCallID: callID, AssistantMessageID: assistantID,
			Outcome: api.ToolResultOutcomeCompleted, Content: `{"need":"run the local service","loaded":["command"]}`,
			Invocation: &api.InvocationReceipt{Tool: "request_tools", ToolCallID: callID, Owner: "tool_surface", Status: api.InvocationStatusCompleted, Invoked: true},
		}},
	))
	// request_tools records the standing set it produced.
	_, err := fixture.Store.PutTurnLoadReceipt(t.Context(), store.TurnLoadReceipt{
		SessionID: fixture.Sess.ID, Trigger: store.TurnLoadTriggerRequest, ToolCallID: callID,
		Decisions: "{}", StateJSON: "{}",
		Standing: `{"tools":[{"tool":"command","source":"requested","need":"run the local service"}]}`,
	})
	testutil.FailErr(t, "record request receipt", err)
	// A host restart drops the activation cache while retaining the store.
	fixture.Mgr.Coordinator.Loading.SetLedger(turnload.NewLedger())
	_, err = fixture.Mgr.Submissions.Prompt(t.Context(), fixture.Sess.ID, "continue after restart")
	testutil.FailErr(t, "resume persisted session", err)
	for _, meta := range recorder.LastRequest().Tools {
		if meta.Name == "command" {
			if len(meta.ArgsSchema) == 0 {
				t.Fatal("restored command lacks its current argument schema")
			}
			return
		}
	}
	t.Fatal("first resumed provider request omitted the previously loaded command schema")
}
