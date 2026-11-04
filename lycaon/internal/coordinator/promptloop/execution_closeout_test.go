package promptloop

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
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
	deps.Closeout.BeforeFinishNoToolTurn = func(_ context.Context, _ *api.Session, _ []api.Message, _, _ string, _ guidance.CloseoutDraft, _ string, _ bool, _ []string, _ bool) (*guidance.Refusal, bool) {
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

// AC-MID-10, MC-INV-16: through the real store, text with no note row is debt after
// a restart, and a retry after the note committed reuses it. A storage failure
// leaves no row and no claim of publication.
func TestMidTurnNoteAcrossARestartOnTheRealStore(t *testing.T) {
	ctx := t.Context()
	db := testdbfixture.OpenPath(t, filepath.Join(t.TempDir(), "store.db"))
	testdbseed.InsertProjectRoot(t, db, testdbseed.DefaultProjectID, t.TempDir())
	messages := store.NewSQL(db)
	sess, err := messages.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	carrier := api.Message{ID: uuid.NewString(), Role: api.MessageRoleAssistant, Origin: api.MessageOriginModel, Content: "Starting now.",
		ToolCalls: []api.ToolCall{{ID: "call-1", Name: "recall", Args: map[string]any{"path": "a.go"}}}}
	testutil.FailErr(t, "append prompt and carrier", messages.AppendMessages(ctx, sess.ID, midTurnUser(), carrier))

	// Each incarnation holds nothing but what it reads back.
	restart := func() (*midTurnHarness, []api.Message) {
		h := newMidTurnHarness(t, publishingSurface)
		h.sess = sess
		h.loop.Projection.Deps.AppendMessages = func(ctx context.Context, sessionID string, msgs ...api.Message) error {
			for _, msg := range msgs {
				if h.appendFn != nil {
					if err := h.appendFn(msg); err != nil {
						return err
					}
				}
				if api.IsAgentNoteMessage(msg) {
					h.order = append(h.order, "note")
				}
			}
			return messages.AppendMessages(ctx, sessionID, msgs...)
		}
		history, err := messages.GetMessages(ctx, sess.ID)
		testutil.FailErr(t, "reload", err)
		return h, history
	}
	noteRows := func(history []api.Message) int {
		n := 0
		for _, msg := range history {
			if msg.ID == api.MidTurnNoteID(carrier.ID) {
				n++
			}
		}
		return n
	}
	run := func(h *midTurnHarness, history []api.Message) ([]api.Message, error) {
		out, _, _, _, _, _, err := h.loop.Batch.executeToolCallsInTurn(ctx, sess, sess.ID, carrier.ToolCalls,
			tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID, Agent: "coordinator"}}, history, "go", carrier.ID, publishingSurface, midTurnState(t, publishingSurface))
		return out, err
	}

	// A commit failure ends the batch before any tool runs and leaves the text owed.
	h, history := restart()
	h.appendFn = func(msg api.Message) error {
		if api.IsAgentNoteMessage(msg) {
			return errors.New("disk full")
		}
		return nil
	}
	if _, err := run(h, history); err == nil {
		t.Fatal("a failed note commit must end the batch")
	}
	if len(h.order) != 0 {
		t.Fatalf("ran after the failed commit: %v", h.order)
	}
	_, history = restart()
	if noteRows(history) != 0 || !guard.DeriveWaitVisibility(history).Pending() {
		t.Fatalf("a crash before the commit left notes = %d, debt = %v", noteRows(history), guard.DeriveWaitVisibility(history).Pending())
	}

	// The first attempt commits the note and runs the tool once.
	h, history = restart()
	if _, err := run(h, history); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	if len(h.order) != 2 || h.order[0] != "note" || h.order[1] != "recall" {
		t.Fatalf("order = %v", h.order)
	}
	_, history = restart()
	if noteRows(history) != 1 || guard.DeriveWaitVisibility(history).Pending() {
		t.Fatalf("after commit: notes = %d, debt = %v", noteRows(history), guard.DeriveWaitVisibility(history).Pending())
	}

	// A retry of the same carrier after a restart adds no second note.
	h, history = restart()
	if _, err := run(h, history); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(h.order) > 0 && h.order[0] == "note" {
		t.Fatalf("retry published again: %v", h.order)
	}
	_, history = restart()
	if noteRows(history) != 1 {
		t.Fatalf("notes after retry = %d", noteRows(history))
	}
}
