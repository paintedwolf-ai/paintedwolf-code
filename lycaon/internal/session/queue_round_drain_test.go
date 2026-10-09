package session

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestQueuedPromptDispatchesWhenRoundCompletesAfterTurnEndCheck(t *testing.T) {
	ctx := context.Background()
	mgr, st := newTestManager(t)
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	dir := t.TempDir()
	testdbseed.BindSessionWorkspace(t, st, sess.ID, dir)
	workers := &queueRoundWorkerQueue{jobs: []api.WorkerTask{{
		ID: "job-1", ParentSessionID: sess.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: dir, Status: api.WorkerStatusRunning,
	}}}
	mgr.SetWorkerQueue(workers)

	row, _, err := mgr.Submissions.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "second", promptinput.Input{Text: "second of two"})
	testutil.FailErr(t, "admit prompt", err)
	_, err = mgr.Submissions.RunPromptSubmission(ctx, row.ID)
	testutil.FailErr(t, "dispatch check with open round", err)
	waiting, err := mgr.Submissions.GetPromptSubmission(ctx, row.ID)
	testutil.FailErr(t, "get waiting receipt", err)
	if waiting.Status != store.PromptSubmissionQueued {
		t.Fatalf("receipt status = %s, want queued while the worker cycle runs", waiting.Status)
	}

	workers.jobs[0].Status = api.WorkerStatusComplete
	mgr.Runner.Coordinator.CoordinatorLoop().DrainPending(ctx, sess.ID)
	mgr.WaitForCoordinatorAsyncTurns(ctx)

	closed, err := mgr.Submissions.GetPromptSubmission(ctx, row.ID)
	testutil.FailErr(t, "get drained receipt", err)
	if closed.Status != store.PromptSubmissionComplete {
		t.Fatalf("receipt status = %s, want complete once the round completes", closed.Status)
	}
	if draft := mgr.queue.Snapshot(sess.ID); len(draft.QueueItems) != 0 {
		t.Fatalf("draft retained items after round-end drain: %+v", draft.QueueItems)
	}

	// A later quiescence finds nothing queued and does not replay the turn.
	mgr.Runner.Coordinator.CoordinatorLoop().DrainPending(ctx, sess.ID)
	mgr.WaitForCoordinatorAsyncTurns(ctx)
	msgs, err := mgr.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	userTurns := 0
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleUser && msg.Content == "second of two" {
			userTurns++
		}
	}
	if userTurns != 1 {
		t.Fatalf("queued prompt dispatched %d times, want exactly once", userTurns)
	}
}
