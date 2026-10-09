package wiring

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type batchRunLoopWF struct {
	run  *api.WorkflowRun
	vars map[string]any
}

func (s batchRunLoopWF) ActiveRun(context.Context, string) (*api.WorkflowRun, error) {
	return s.run, nil
}

func (s batchRunLoopWF) ScaffoldVars(context.Context, string) (map[string]any, error) {
	if s.vars != nil {
		return s.vars, nil
	}
	return map[string]any{}, nil
}

func (s batchRunLoopWF) HumanApprovalAwaiting(context.Context, string) (bool, error) {
	return scaffoldvars.HumanApprovalAwaiting(s.vars), nil
}

func (batchRunLoopWF) HostObligationHeld(context.Context, string) (bool, error) {
	return false, nil
}

func (batchRunLoopWF) HostObligationHoldKinds(context.Context, string) []string { return nil }

func TestStackedWakeFactsAreConsumedByOneObservedPrompt(t *testing.T) {
	engine := loopwake.NewLoopEngine()
	t.Cleanup(func() { engine.ForgetSession(context.Background(), "s1") })
	var prompts atomic.Int32
	sess := &api.Session{ID: "s1", Status: api.SessionStatusBusy, WorkspacePath: t.TempDir()}
	synthesizeWF := batchRunLoopWF{
		run: &api.WorkflowRun{ID: "run-batch", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"},
		vars: map[string]any{
			"coordinator_batch": map[string]any{
				"phase": batch.PhaseSynthesize,
				"seq":   2,
			},
		},
	}
	deps := loopwake.LoopDeps{
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
		Limits:               func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		WorkerCycleIdle:      func(context.Context, *api.Session, string) (bool, error) { return true, nil },
		QueueInform:          func(context.Context, string, anchor.ID, anchor.Envelope) {},
	}
	deps.GetSession = func(context.Context, string) (*api.Session, error) { return sess, nil }
	deps.WorkflowSource = synthesizeWF
	deps.RunPrompt = func(context.Context, string) (*promptresult.Result, error) {
		engine.ObservePrompt("s1")(inject.CoordinatorTurnFrame{})
		prompts.Add(1)
		return &promptresult.Result{}, nil
	}
	engine.SetDeps(deps)
	finishExecution := engine.BeginPromptExecution(t.Context(), "s1")
	// Distinct terminal events arrive while synthesis is still in flight.
	for _, jobID := range []string{"implementation-job", "verifier-job"} {
		engine.NudgeAfterWorkerJobTerminal(
			t.Context(), "s1", jobID, anchor.WorkerTaskFinished, anchor.WorkerTaskFinished, "", anchor.Envelope{},
		)
	}
	engine.Nudge(context.Background(), "s1", anchor.WaitTimerFired, anchor.WaitTimerFired, "", anchor.Envelope{})
	if prompts.Load() != 0 {
		t.Fatalf("prompts = %d want 0 while session busy", prompts.Load())
	}
	if _, ok := engine.PendingForTest("s1"); !ok {
		t.Fatal("worker wake should defer while session busy")
	}
	finishExecution()
	engine.WaitForAsyncTurns(testutil.BoundedContext(t, time.Second))
	if got := prompts.Load(); got != 1 {
		t.Fatalf("deferred worker wake prompts = %d want 1 after idle drain", got)
	}
}

func TestInTurnLatchBlocksSecondSynthesis(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeTestProjectApprovalsAllowWrite(t, dir)

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)

	h.SessionMgr.Runner.Settlement.Begin(sess.ID, anchor.InformRender(anchor.WorkerTaskFinished))
	h.SessionMgr.Coordinator.Batch.AcceptSynthesis(ctx, sess.ID)

	guard := h.SessionMgr.Coordinator.Batch.TurnGuard(sess.ID)
	if !guard.SynthesisAcceptedThisTurn {
		t.Fatal("first grounded synthesis should set in-turn latch")
	}
	state := h.SessionMgr.Workers.State.ForSession(ctx, sess)
	if state.BatchPhase != batch.PhaseClosed {
		t.Fatalf("batch_phase = %q want closed after synthesis", state.BatchPhase)
	}
}

func TestStaleBatchSeqWakeDroppedAfterNewUserMessage(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: "first batch", FollowUpText: "Investigating."},
		{Pattern: "second batch", FollowUpText: "New batch started."},
	}})
	h := BuildForTest(t, WithLLMClient(mock))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)

	ctx := context.Background()
	dir := t.TempDir()
	writeTestProjectApprovalsAllowWrite(t, dir)
	_ = writeImplementFixture(t, dir)

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)

	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, "first batch experiment"); err != nil {
		testutil.FailErr(t, "Prompt first batch", err)
	}
	state := h.SessionMgr.Workers.State.ForSession(ctx, sess)
	seqBefore := state.BatchSeq

	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, "second batch after closed synthesis window"); err != nil {
		testutil.FailErr(t, "Prompt second batch", err)
	}
	state = h.SessionMgr.Workers.State.ForSession(ctx, sess)
	if state.BatchSeq <= seqBefore {
		t.Fatalf("batch_seq = %d want > %d after visible user message", state.BatchSeq, seqBefore)
	}
	staleSeq := seqBefore
	if staleSeq <= 0 {
		staleSeq = 1
	}

	msgsBefore, err := h.SessionMgr.Runner.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages before stale wake", err)

	h.SessionMgr.Coordinator.Runtime.CoordinatorLoop().Nudge(
		ctx,
		sess.ID,
		anchor.WaitTimerFired,
		anchor.WaitTimerFired,
		"",
		anchor.Envelope{BatchSeq: staleSeq, BatchSeqSet: true},
	)
	h.SessionMgr.Runner.Coordinator.CoordinatorLoop().DrainPending(ctx, sess.ID)
	h.SessionMgr.Coordinator.WaitForTurns(testutil.BoundedContext(t, 5*time.Second))

	msgsAfter, err := h.SessionMgr.Runner.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages after stale wake", err)
	if len(msgsAfter) != len(msgsBefore) {
		t.Fatalf("stale batch_seq scheduled wake appended messages: before=%d after=%d", len(msgsBefore), len(msgsAfter))
	}
	if id, ok := h.SessionMgr.Runner.Coordinator.Kicks().PeekPendingKickID(sess.ID); ok && id == anchor.InformRender(anchor.WaitTimerFired) {
		t.Fatal("stale batch_seq wake must not leave scheduled kick queued")
	}
}
