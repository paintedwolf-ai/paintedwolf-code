package promptloop

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"path/filepath"
	"reflect"
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
	deps.Model.LLM = client
	deps.Context.Policy = softStopToolPolicy{}
	deps.Context.Tools = tools.NewStubRegistry()
	deps.Projection.AdmitModelResponse = messages.AdmitModelResponse
	deps.Projection.SettleModelOutput = messages.SettleModelOutput
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

func TestForcedCloseoutRepairPreservesPersistedIterationIdentity(t *testing.T) {
	ctx := t.Context()
	directory := t.TempDir()
	db := testdbfixture.OpenPath(t, filepath.Join(directory, "store.db"))
	testdbseed.InsertProjectRoot(t, db, testdbseed.DefaultProjectID, directory)
	messages := store.NewSQL(db)
	sess, err := messages.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	execution, err := messages.BeginTurn(ctx, store.TurnStart{SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: store.TurnOriginUser, InputJSON: "{}"})
	testutil.FailErr(t, "begin turn", err)
	gateOpen := true
	registry := tools.NewStubRegistry()
	testutil.FailErr(t, "register command", registry.Register("command", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		gateOpen = false
		return "required evidence", nil
	}))
	client := &softStopSequenceLLM{completions: []*modelcall.Completion{
		{Content: "Premature closeout"},
		{ToolCalls: []api.ToolCall{{ID: "repair-read", Name: "command", Args: map[string]any{"command": "cat a.txt"}}}},
		{Content: "Final report"},
	}}
	deps := PromptLoopDeps{
		Context: ContextDeps{BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
			return history, nil
		},
			ProjectRootCount: func(context.Context, *api.Session) int { return 1 }},
		Projection: ProjectionDeps{
			AppendMessages: messages.AppendMessages,
			UpdateMessage: func(ctx context.Context, sessionID, messageID string, message api.Message) error {
				_, err := messages.UpdateMessage(ctx, sessionID, messageID, message)
				return err
			},
			Streams: &testMessageStreams{project: func(ctx context.Context, sessionID string, message api.Message) error {
				return messages.PatchLiveProjection(ctx, sessionID, message.ID, message.Content, message.ToolCalls)
			}},
			AppendDraftVersion: messages.AppendDraftVersion, CountDraftVersions: messages.CountDraftVersions,
		},
	}
	deps.Model.LLM = client
	deps.Context.Policy = &recordingToolPolicy{}
	deps.Context.Tools = registry
	deps.Context.PromptTurnSurface = func(string) string { return "implement_investigate" }
	deps.Projection.SettleModelOutput = messages.SettleModelOutput
	deps.Closeout.BeforeFinishNoToolTurn = func(_ context.Context, _ *api.Session, _ []api.Message, _, _, _ string, _ bool, _ []string, _ bool) (*guidance.Refusal, bool) {
		if !gateOpen {
			return nil, false
		}
		return guidance.NewRefusal("WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT", "Required workflow evidence remains open").WithCause(errors.Join(guidance.ErrCloseoutStopped, guidance.ErrCloseoutNeedsRepairTurn)), true
	}
	loop := NewPromptLoopForTest(deps)
	in := PromptRunInput{SessionID: sess.ID, Session: sess, TurnID: execution.Turn.ID, AttemptID: execution.Attempt.ID,
		History: []api.Message{{Role: api.MessageRoleUser, Content: "Complete the workflow"}}, ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID}},
	}
	setup, err := loop.preparePromptLoop(ctx, in)
	testutil.FailErr(t, "prepare", err)
	_, _, _, blocked := loop.Closeout.runEarlyTurnCloseout(ctx, sess, sess.ID, in.ProfileID, "Complete the workflow", setup.state.history, 1,
		TurnCloseoutIterationCap, "", false, in, setup.state)
	if !errors.Is(blocked, guidance.ErrCloseoutNeedsRepairTurn) {
		t.Fatalf("closeout=%v", blocked)
	}
	repair, err := loop.prepareCloseoutRepair(ctx, setup, blocked)
	testutil.FailErr(t, "prepare repair", err)
	if !repair {
		t.Fatal("first forced closeout did not grant repair")
	}
	_, err = loop.runPreparedPromptLoop(ctx, in, setup)
	testutil.FailErr(t, "run repair with SQL output uniqueness", err)
	rows, err := db.QueryContext(ctx, "SELECT iteration FROM model_outputs WHERE turn_attempt_id=? ORDER BY iteration", execution.Attempt.ID)
	testutil.FailErr(t, "read model iterations", err)
	defer rows.Close()
	var iterations []int
	for rows.Next() {
		var iteration int
		testutil.FailErr(t, "scan iteration", rows.Scan(&iteration))
		iterations = append(iterations, iteration)
	}
	testutil.FailErr(t, "iterate", rows.Err())
	if !reflect.DeepEqual(iterations, []int{1, 2, 3}) || gateOpen {
		t.Fatalf("iterations=%v gateOpen=%t", iterations, gateOpen)
	}
}
