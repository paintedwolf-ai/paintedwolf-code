package promptloop

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromptLoopResponseAdmissionStopsBeforeAnotherRequest(t *testing.T) {
	ctx := t.Context()
	messages := store.NewMemory()
	session, err := messages.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "project")
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "configure response allowance", messages.ConfigureModelLimit(ctx, session.ID, 1))
	execution, err := messages.BeginTurn(ctx, store.TurnStart{SessionID: session.ID, ProjectID: session.ProjectID, Origin: store.TurnOriginUser, InputJSON: "{}"})
	testutil.FailErr(t, "admit user turn", err)
	client := &softStopSequenceLLM{completions: []*modelcall.Completion{
		{ToolCalls: []api.ToolCall{{Name: "read", ID: "read", Args: map[string]any{"path": "a"}}}},
		{Content: "This request must not run."},
	}}
	deps := StoreDeps(messages)
	deps.LLM = client
	deps.Policy = softStopToolPolicy{}
	deps.Tools = tools.NewStubRegistry()
	deps.AdmitModelResponse = messages.AdmitModelResponse
	deps.SettleModelOutput = messages.SettleModelOutput
	_, err = NewPromptLoopForTest(deps).Run(ctx, PromptRunInput{
		SessionID: session.ID, Session: session, TurnID: execution.Turn.ID, AttemptID: execution.Attempt.ID,
		History: []api.Message{{Role: api.MessageRoleUser, Content: "Read a."}}, ProfileID: "implement",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: session.ID},
		},
	})
	if !errors.Is(err, store.ErrModelResponseLimit) || client.index != 1 {
		t.Fatalf("response limit: calls=%d error=%v", client.index, err)
	}
}
