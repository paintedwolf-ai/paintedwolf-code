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

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
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
		out, err := h.loop.Batch.executeToolCallsInTurn(ctx, sess, sess.ID, carrier.ToolCalls,
			tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID, Agent: "coordinator"}}, history, "go", carrier.ID, publishingSurface, midTurnState(t, publishingSurface))
		return out.history, err
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

type scriptedModelStep struct {
	completion *modelcall.Completion
	err        error
}

type scriptedStepLLM struct {
	steps []scriptedModelStep
	index int
}

func (s *scriptedStepLLM) Complete(_ context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if s.index >= len(s.steps) {
		return &modelcall.Completion{Content: "done"}, nil
	}
	step := s.steps[s.index]
	s.index++
	if step.err != nil {
		return nil, step.err
	}
	return step.completion, nil
}

func (s *scriptedStepLLM) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	completion, err := s.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: completion.Content, ToolCalls: completion.ToolCalls, Done: true}
	close(ch)
	return ch, nil
}

type attemptHarness struct {
	messages          *store.SQL
	sess              *api.Session
	execution         store.TurnExecution
	deps              PromptLoopDeps
	settledIterations *[]int
	seenIterations    map[int]int
}

func newAttemptHarness(t *testing.T, workerChild bool, maxIter int, steps []scriptedModelStep) (*attemptHarness, *scriptedStepLLM) {
	t.Helper()
	ctx := t.Context()
	directory := t.TempDir()
	db := testdbfixture.OpenPath(t, filepath.Join(directory, "store.db"))
	testdbseed.InsertProjectRoot(t, db, testdbseed.DefaultProjectID, directory)
	messages := store.NewSQL(db)
	sess, err := messages.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	if workerChild {
		parent, err := messages.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		testutil.FailErr(t, "create parent session", err)
		sess, err = messages.CreateChild(ctx, parent, api.SpawnChildRequest{
			AgentType: "implementer", MaxToolLoops: maxIter,
		})
		testutil.FailErr(t, "create worker child session", err)
	}
	execution, err := messages.BeginTurn(ctx, store.TurnStart{
		SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: store.TurnOriginUser, InputJSON: "{}",
	})
	testutil.FailErr(t, "begin turn", err)

	registry := tools.NewStubRegistry()
	testutil.FailErr(t, "register command", registry.Register("command", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "file contents", nil
	}))
	client := &scriptedStepLLM{steps: steps}
	var settledIterations []int
	seenIterations := make(map[int]int)

	deps := PromptLoopDeps{
		Context: ContextDeps{
			Limits: func(context.Context, *api.Session) settings.SessionLimits {
				lim := settings.DefaultSessionLimits()
				lim.MaxIterations = maxIter
				return lim
			},
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
			ProjectRootCount:   func(context.Context, *api.Session) int { return 1 },
			Policy:             &recordingToolPolicy{},
			Tools:              registry,
			ContextSurfaceDeps: ContextSurfaceDeps{PromptTurnSurface: func(string) string { return "implement_investigate" }},
		},
		Model: ModelDeps{LLM: client},
		Projection: ProjectionDeps{
			AppendMessages: messages.AppendMessages,
			UpdateMessage: func(ctx context.Context, sessionID, messageID string, message api.Message) error {
				_, err := messages.UpdateMessage(ctx, sessionID, messageID, message)
				return err
			},
			Streams: &testMessageStreams{project: func(ctx context.Context, sessionID string, message api.Message) error {
				return messages.PatchLiveProjection(ctx, sessionID, message.ID, message.Content, message.ToolCalls)
			}},
			AppendDraftVersion: messages.AppendDraftVersion,
			CountDraftVersions: messages.CountDraftVersions,
			AdmitModelResponse: messages.AdmitModelResponse,
			SettledModelOutputIteration: func(ctx context.Context, attemptID string) (int, error) {
				return store.SettledModelOutputIteration(ctx, messages, attemptID)
			},
			SettleModelOutput: func(ctx context.Context, out store.ModelOutput) (store.ModelOutput, error) {
				seenIterations[out.Iteration]++
				settledIterations = append(settledIterations, out.Iteration)
				return messages.SettleModelOutput(ctx, out)
			},
		},
	}
	return &attemptHarness{
		messages:          messages,
		sess:              sess,
		execution:         execution,
		deps:              deps,
		settledIterations: &settledIterations,
		seenIterations:    seenIterations,
	}, client
}

func (h *attemptHarness) runAttempt(t *testing.T, prompt, profileID, workerJobID string) error {
	t.Helper()
	_, err := NewPromptLoopForTest(h.deps).Run(t.Context(), PromptRunInput{
		SessionID: h.sess.ID, Session: h.sess, TurnID: h.execution.Turn.ID, AttemptID: h.execution.Attempt.ID,
		History:   []api.Message{{Role: api.MessageRoleUser, Content: prompt}},
		ProfileID: profileID,
		ToolCtx:   tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: h.sess.ID, WorkerJobID: workerJobID}},
	})
	return err
}

func (h *attemptHarness) assertStrictlyIncreasingIterations(t *testing.T, wantCount int) {
	t.Helper()
	rows, err := h.messages.GetMessages(t.Context(), h.sess.ID)
	testutil.FailErr(t, "read settled fixture transcript", err)
	for _, row := range rows {
		if row.ToolResult != nil && row.ToolResult.Outcome == api.ToolResultOutcomeRejected {
			t.Fatalf("scripted acting turn never reached its tool: %+v", row.ToolResult)
		}
	}
	got := *h.settledIterations
	if len(got) != wantCount {
		t.Fatalf("settled iterations = %v (len %d), want %d settled outputs", got, len(got), wantCount)
	}
	for i, iter := range got {
		if iter <= 0 {
			t.Fatalf("settled iteration[%d] = %d, want >= 1", i, iter)
		}
		if h.seenIterations[iter] != 1 {
			t.Fatalf("iteration %d settled %d times in %v", iter, h.seenIterations[iter], got)
		}
		if i > 0 && iter <= got[i-1] {
			t.Fatalf("settled iterations %v are not strictly increasing at index %d", got, i)
		}
	}
}

func TestSinglePerAttemptIterationAllocatorAcrossMultiCallPaths(t *testing.T) {
	if got := nextModelIteration(nil); got != 1 {
		t.Fatalf("nil state nextModelIteration = %d, want 1", got)
	}
	readCall := func(id string) *modelcall.Completion {
		return &modelcall.Completion{
			ToolCalls: []api.ToolCall{{ID: id, Name: "command", Args: map[string]any{"command": "cat a.txt"}}},
		}
	}
	textReply := func(text string) *modelcall.Completion {
		return &modelcall.Completion{Content: text}
	}

	t.Run("seeded_worker_progress", func(t *testing.T) {
		h, client := newAttemptHarness(t, true, 3, []scriptedModelStep{
			{completion: readCall("worker-read-1")},
			{completion: textReply("Worker closeout summary.")},
		})
		h.deps.Nudges.WorkerJob = func(_ context.Context, workerJobID string) (*api.WorkerTask, bool) {
			return &api.WorkerTask{ID: workerJobID, ToolLoopsUsed: 1, MaxToolLoops: 3}, true
		}
		h.deps.Control.WorkerGracefulCancelPending = func(context.Context, *api.Session) (string, bool) {
			return "canceled after seeded round", client.index >= 1
		}
		testutil.FailErr(t, "run seeded worker progress", h.runAttempt(t, "Continue worker task.", "implementer", "job-1"))
		h.assertStrictlyIncreasingIterations(t, 2)
	})

	t.Run("withheld_secret_retry", func(t *testing.T) {
		h, _ := newAttemptHarness(t, false, 3, []scriptedModelStep{
			{completion: readCall("read-before-secret")},
			{err: &llm.ModelRequestSecretWithheldError{Guidance: "use staging token"}},
			{completion: readCall("read-after-secret")},
			{completion: textReply("Completed after withheld secret retry.")},
		})
		h.deps.Nudges.SecretWithheldNudge = func(_ context.Context, _ *api.Session, guidanceText string) HostNudge {
			return HostNudge{Content: "Secret withheld: " + guidanceText}
		}
		testutil.FailErr(t, "run withheld secret retry", h.runAttempt(t, "Deploy with token.", "coordinator", ""))
		h.assertStrictlyIncreasingIterations(t, 3)
	})

	t.Run("user_send_mid_turn", func(t *testing.T) {
		h, client := newAttemptHarness(t, false, 3, []scriptedModelStep{
			{completion: textReply("Draft before user send.")},
			{completion: readCall("read-after-user-send")},
			{completion: textReply("Final answer after user send.")},
		})
		sentOnce := false
		h.deps.Inbox.TakeUserSend = func(context.Context, string) ([]api.Message, error) {
			if client.index == 1 && !sentOnce {
				sentOnce = true
				return []api.Message{{ID: uuid.NewString(), Role: api.MessageRoleUser, Content: "Wait, also check a.txt."}}, nil
			}
			return nil, nil
		}
		testutil.FailErr(t, "run user send mid-turn", h.runAttempt(t, "Start task.", "coordinator", ""))
		h.assertStrictlyIncreasingIterations(t, 3)
	})

	t.Run("early_closeout_after_provider_failure", func(t *testing.T) {
		h, _ := newAttemptHarness(t, true, 3, []scriptedModelStep{
			{completion: readCall("read-before-timeout")},
			{err: ErrLLMTurnTimeout},
			{completion: textReply("Early closeout after provider timeout.")},
		})
		h.deps.Nudges.WorkerJob = func(_ context.Context, workerJobID string) (*api.WorkerTask, bool) {
			return &api.WorkerTask{ID: workerJobID, ToolLoopsUsed: 1, MaxToolLoops: 3}, true
		}
		testutil.FailErr(t, "run early closeout after provider failure", h.runAttempt(t, "Investigate timeout.", "implementer", "job-timeout"))
		h.assertStrictlyIncreasingIterations(t, 2)
	})

	t.Run("iteration_cap_closeout", func(t *testing.T) {
		h, client := newAttemptHarness(t, false, 2, []scriptedModelStep{
			{completion: readCall("cap-read-1")},
			{completion: textReply("Draft blocked at final round.")},
			{completion: textReply("Iteration cap closeout summary.")},
		})
		h.deps.Closeout.BeforeFinishNoToolTurn = func(_ context.Context, _ *api.Session, _ []api.Message, _, _, _ string, _ bool, _ []string, _ bool) (*guidance.Refusal, bool) {
			if client.index == 2 {
				return guidance.NewRefusal("INCOMPLETE_SUMMARY", "Provide a complete summary"), true
			}
			return nil, false
		}
		testutil.FailErr(t, "run iteration cap closeout", h.runAttempt(t, "Read files until cap.", "coordinator", ""))
		h.assertStrictlyIncreasingIterations(t, 3)
	})

	t.Run("closeout_gate_repair", func(t *testing.T) {
		h, _ := newAttemptHarness(t, false, 3, []scriptedModelStep{
			{completion: readCall("initial-read")},
			{completion: textReply("Premature closeout before gate closed.")},
			{completion: readCall("repair-read")},
			{completion: textReply("Final report after gate repair.")},
		})
		readCount := 0
		testutil.FailErr(t, "override read tool", h.deps.Context.Tools.Register("command", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			readCount++
			return "evidence", nil
		}))
		h.deps.Closeout.BeforeFinishNoToolTurn = func(_ context.Context, _ *api.Session, _ []api.Message, _, _, _ string, _ bool, _ []string, _ bool) (*guidance.Refusal, bool) {
			if readCount >= 2 {
				return nil, false
			}
			return guidance.NewRefusal("WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT", "Required workflow evidence remains open").
				WithCause(errors.Join(guidance.ErrCloseoutStopped, guidance.ErrCloseoutNeedsRepairTurn)), true
		}
		testutil.FailErr(t, "run closeout gate repair", h.runAttempt(t, "Finish with evidence.", "coordinator", ""))
		h.assertStrictlyIncreasingIterations(t, 4)
	})

	t.Run("resumed_attempt_with_settled_outputs", func(t *testing.T) {
		h, _ := newAttemptHarness(t, false, 2, []scriptedModelStep{
			{completion: readCall("resumed-read-1")},
			{completion: textReply("Completed on resumed attempt.")},
		})
		for prior := 1; prior <= 2; prior++ {
			_, err := h.deps.Projection.SettleModelOutput(t.Context(), store.ModelOutput{
				ID: uuid.NewString(), TurnAttemptID: h.execution.Attempt.ID, SessionID: h.sess.ID,
				Iteration: prior, MessageID: uuid.NewString(), Content: "Prior settled output.",
			})
			testutil.FailErr(t, "seed prior settled output", err)
		}
		testutil.FailErr(t, "run resumed attempt", h.runAttempt(t, "Resume turn.", "coordinator", ""))
		h.assertStrictlyIncreasingIterations(t, 4)
		if !reflect.DeepEqual(*h.settledIterations, []int{1, 2, 3, 4}) {
			t.Fatalf("resumed attempt iterations = %v, want [1 2 3 4]", *h.settledIterations)
		}
	})

	t.Run("settled_iteration_lookup_error", func(t *testing.T) {
		h, _ := newAttemptHarness(t, false, 2, nil)
		wantErr := errors.New("settled iteration lookup failed")
		h.deps.Projection.SettledModelOutputIteration = func(context.Context, string) (int, error) {
			return 0, wantErr
		}
		if err := h.runAttempt(t, "Resume turn.", "coordinator", ""); !errors.Is(err, wantErr) {
			t.Fatalf("run error = %v, want %v", err, wantErr)
		}
	})
}
