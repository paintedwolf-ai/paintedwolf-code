package wiring

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
	"slices"
	"testing"
)

func TestInTurnLatchBlocksSecondSynthesis(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeTestProjectApprovalsAllowWrite(t, dir)

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)

	h.Sessions.Manager.Coordinator.Batch.BeginTurn(sess.ID)
	h.Sessions.Manager.Coordinator.Runtime.BeginPromptTurn(sess.ID, anchor.InformRender(anchor.WorkerTaskFinished))
	if code := batchClosedRefusal(t, h, ctx, sess); code == guard.CoordinatorBatchAlreadyClosedCode {
		t.Fatal("the batch refused a report before any synthesis was accepted")
	}

	h.Sessions.Manager.Coordinator.Batch.AcceptSynthesis(ctx, sess.ID)
	first := h.Sessions.Manager.Workers.State.ForSession(ctx, sess)
	if first.BatchPhase != batch.PhaseClosed {
		t.Fatalf("batch_phase = %q want closed after synthesis", first.BatchPhase)
	}
	if code := batchClosedRefusal(t, h, ctx, sess); code != guard.CoordinatorBatchAlreadyClosedCode {
		t.Fatalf("second report in the same turn: refusal code = %q, want %s", code, guard.CoordinatorBatchAlreadyClosedCode)
	}

	h.Sessions.Manager.Coordinator.Batch.AcceptSynthesis(ctx, sess.ID)
	second := h.Sessions.Manager.Workers.State.ForSession(ctx, sess)
	if second.BatchPhase != first.BatchPhase || second.BatchSeq != first.BatchSeq {
		t.Fatalf("second synthesis moved the batch: phase %q seq %d, want %q seq %d", second.BatchPhase, second.BatchSeq, first.BatchPhase, first.BatchSeq)
	}
}

// batchClosedRefusal runs the coordinator pre-invoke guard for a board report
// and returns its typed refusal code, empty when admitted.
func batchClosedRefusal(t *testing.T, h *Harness, ctx context.Context, sess *api.Session) string {
	t.Helper()
	_, _, err := h.Sessions.Manager.Coordinator.Guards.BeforeTool(ctx, sess, nil, "", "pack_board", map[string]any{})
	if reject := toolrejection.AsToolReject(err); reject != nil {
		return reject.Code
	}
	return ""
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

	if _, err := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "first batch experiment"); err != nil {
		testutil.FailErr(t, "Prompt first batch", err)
	}
	state := h.Sessions.Manager.Workers.State.ForSession(ctx, sess)
	seqBefore := state.BatchSeq

	if _, err := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "second batch after closed synthesis window"); err != nil {
		testutil.FailErr(t, "Prompt second batch", err)
	}
	state = h.Sessions.Manager.Workers.State.ForSession(ctx, sess)
	if state.BatchSeq <= seqBefore {
		t.Fatalf("batch_seq = %d want > %d after visible user message", state.BatchSeq, seqBefore)
	}
	staleSeq := seqBefore
	if staleSeq <= 0 {
		staleSeq = 1
	}

	msgsBefore, err := h.Sessions.Manager.Runner.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages before stale wake", err)

	h.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Nudges.Nudge(
		ctx,
		sess.ID,
		anchor.WaitTimerFired,
		anchor.WaitTimerFired,
		"",
		anchor.Envelope{BatchSeq: staleSeq, BatchSeqSet: true},
	)
	h.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Nudges.DrainPending(ctx, sess.ID)
	h.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Turns.WaitForAsyncTurns(t.Context())

	msgsAfter, err := h.Sessions.Manager.Runner.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages after stale wake", err)
	if len(msgsAfter) != len(msgsBefore) {
		t.Fatalf("stale batch_seq scheduled wake appended messages: before=%d after=%d", len(msgsBefore), len(msgsAfter))
	}
	if slices.Contains(h.Sessions.Manager.Coordinator.Guidance.PendingIDs(ctx, sess.ID), anchor.InformRender(anchor.WaitTimerFired)) {
		t.Fatal("stale batch_seq wake must not leave scheduled kick queued")
	}
}
