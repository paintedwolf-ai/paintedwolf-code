package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubSessionWorkerAbort struct {
	called   bool
	session  string
	project  string
	sessions []string
}

type stubSessionWorkflowStop struct {
	sessions []string
}

func (s *stubSessionWorkflowStop) StopSession(_ context.Context, sessionID, _ string) error {
	s.sessions = append(s.sessions, sessionID)
	return nil
}

func (s *stubSessionWorkerAbort) AbortAllWorkers(_ context.Context, sessionID, projectDir, _ string) error {
	s.called = true
	s.session = sessionID
	s.project = projectDir
	s.sessions = append(s.sessions, sessionID)
	return nil
}

func TestAbortStopsEntireSessionTreeAndClearsQueuedTurns(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	mgr := NewManager(st, nil, nil, settings.DefaultSessionLimits())
	abort := &stubSessionWorkerAbort{}
	workflows := &stubSessionWorkflowStop{}
	warmer := &fakeIndexWarmer{}
	mgr.SetSessionWorkerAbort(abort)
	mgr.SetSessionWorkflowStop(workflows)
	mgr.SetIndexWarmer(warmer)
	root, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create root", err)
	child, err := st.CreateChild(ctx, root, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)
	const queuedReceiptID = "queued-child-turn"
	_, _, err = st.PutPromptSubmission(ctx, store.PromptSubmission{
		ID: queuedReceiptID, SessionID: child.ID, ProjectID: child.ProjectID,
		InputDigest: "digest", InputJSON: `{"text":"next turn"}`,
		Origin: store.PromptSubmissionOriginUser, SubmittedBy: child.OwnerPersonID,
	})
	testutil.FailErr(t, "put queued prompt receipt", err)
	const runningReceiptID = "running-root-turn"
	_, _, err = st.PutPromptSubmission(ctx, store.PromptSubmission{
		ID: runningReceiptID, SessionID: root.ID, ProjectID: root.ProjectID,
		InputDigest: "running-digest", InputJSON: `{"text":"active turn"}`,
		Origin: store.PromptSubmissionOriginUser, SubmittedBy: root.OwnerPersonID,
	})
	testutil.FailErr(t, "put running prompt receipt", err)
	_, claimed, err := st.ClaimPromptSubmission(ctx, runningReceiptID)
	testutil.FailErr(t, "claim running prompt receipt", err)
	if !claimed {
		t.Fatal("running prompt receipt was not claimed")
	}
	beforeQueueRevision := map[string]uint64{}
	for _, id := range []string{root.ID, child.ID} {
		testutil.FailErr(t, "mark tree busy", st.SetSessionStatus(ctx, id, api.SessionStatusBusy))
		mgr.queue.AppendOrdered(id, "queued-"+id, testutil.HostOwner().ID, "next turn", 0, time.Time{})
		beforeQueueRevision[id] = mgr.QueueSnapshot(id).Revision
	}

	testutil.FailErr(t, "abort child tree", mgr.Abort(ctx, child.ID, "user stopped"))
	for _, id := range []string{root.ID, child.ID} {
		sess, err := st.Get(ctx, id)
		testutil.FailErr(t, "get stopped session", err)
		if sess.Status != api.SessionStatusIdle {
			t.Fatalf("session %s status = %q, want idle", id, sess.Status)
		}
		if draft := mgr.QueueSnapshot(id); len(draft.QueueItems) != 0 {
			t.Fatalf("session %s queue = %+v, want empty", id, draft)
		} else if draft.Revision <= beforeQueueRevision[id] {
			t.Fatalf("session %s queue revision = %d, want newer than %d", id, draft.Revision, beforeQueueRevision[id])
		}
	}
	if len(abort.sessions) != 4 {
		t.Fatalf("worker abort sessions = %v, want initial and final sweeps for root and child", abort.sessions)
	}
	workerSweeps := map[string]int{}
	for _, sessionID := range abort.sessions {
		workerSweeps[sessionID]++
	}
	if workerSweeps[root.ID] != 2 || workerSweeps[child.ID] != 2 {
		t.Fatalf("worker abort sessions = %v, want two sweeps per tree session", abort.sessions)
	}
	if len(workflows.sessions) != 2 || workflows.sessions[0] != child.ID || workflows.sessions[1] != root.ID {
		t.Fatalf("workflow stop sessions = %v, want child-first tree", workflows.sessions)
	}
	if cancels := warmer.cancelsSeen(); len(cancels) != 2 || cancels[0] != child.ID || cancels[1] != root.ID {
		t.Fatalf("index warm stops = %v, want child-first tree", cancels)
	}
	receipt, err := st.GetPromptSubmission(ctx, queuedReceiptID)
	testutil.FailErr(t, "get canceled prompt receipt", err)
	if receipt.Status != store.PromptSubmissionCanceled {
		t.Fatalf("queued receipt status = %q, want canceled", receipt.Status)
	}
	runningReceipt, err := st.GetPromptSubmission(ctx, runningReceiptID)
	testutil.FailErr(t, "get interrupted prompt receipt", err)
	if runningReceipt.Status != store.PromptSubmissionInterrupted {
		t.Fatalf("running receipt status = %q, want interrupted", runningReceipt.Status)
	}
}

func TestAbortRetainsAddressedQueuedTurn(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	mgr := NewManager(st, nil, nil, settings.DefaultSessionLimits())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	const queuedReceiptID = "queued-new-direction"
	_, _, err = st.PutPromptSubmission(ctx, store.PromptSubmission{
		ID: queuedReceiptID, SessionID: sess.ID, ProjectID: sess.ProjectID,
		InputDigest: "queued-digest", InputJSON: `{"text":"use the new direction"}`,
		Origin: store.PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID,
	})
	testutil.FailErr(t, "put queued prompt receipt", err)
	draft := mgr.queue.AppendOrdered(sess.ID, queuedReceiptID, testutil.HostOwner().ID, "use the new direction", 1, time.Now())

	const runningReceiptID = "running-old-direction"
	_, _, err = st.PutPromptSubmission(ctx, store.PromptSubmission{
		ID: runningReceiptID, SessionID: sess.ID, ProjectID: sess.ProjectID,
		InputDigest: "running-digest", InputJSON: `{"text":"old direction"}`,
		Origin: store.PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID,
	})
	testutil.FailErr(t, "put running prompt receipt", err)
	_, claimed, err := st.ClaimPromptSubmission(ctx, runningReceiptID)
	testutil.FailErr(t, "claim running prompt receipt", err)
	if !claimed {
		t.Fatal("running prompt receipt was not claimed")
	}

	testutil.FailErr(t, "abort with queued direction", mgr.Abort(ctx, sess.ID, "use queued direction"))
	if after := mgr.QueueSnapshot(sess.ID); after.Revision != draft.Revision || len(after.QueueItems) != 1 || after.QueueItems[0].ID != queuedReceiptID {
		t.Fatalf("queue after abort = %+v, want preserved draft %+v", after, draft)
	}
	queued, err := st.GetPromptSubmission(ctx, queuedReceiptID)
	testutil.FailErr(t, "get preserved queued receipt", err)
	if queued.Status != store.PromptSubmissionQueued {
		t.Fatalf("queued receipt status = %q, want queued", queued.Status)
	}
	running, err := st.GetPromptSubmission(ctx, runningReceiptID)
	testutil.FailErr(t, "get interrupted running receipt", err)
	if running.Status != store.PromptSubmissionInterrupted {
		t.Fatalf("running receipt status = %q, want interrupted", running.Status)
	}
}

func (s *stubSessionWorkerAbort) AbortWorkersForRoot(_ context.Context, _, _ string, _ []projectroot.RootRef, _ string) error {
	return nil
}

func TestAbortCancelsWorkersAndMarksIdle(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := NewManager(store, nil, nil, settings.DefaultSessionLimits())
	abort := &stubSessionWorkerAbort{}
	mgr.SetSessionWorkerAbort(abort)

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create session", err)
	if err := store.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy); err != nil {
		testutil.FailErr(t, "SetSessionStatus busy", err)
	}

	if err := mgr.Abort(ctx, sess.ID, ""); err != nil {
		testutil.FailErr(t, "Abort", err)
	}
	if !abort.called {
		t.Fatal("expected worker abort hook")
	}
	if abort.session != sess.ID {
		t.Fatalf("abort session = %q", abort.session)
	}

	got, err := store.Get(ctx, sess.ID)
	testutil.FailErr(t, "Get session", err)
	if got.Status != api.SessionStatusIdle {
		t.Fatalf("status = %q, want idle", got.Status)
	}
}

func TestPromptCancelPropagatesAbort(t *testing.T) {
	ctx := context.Background()
	parent, cancel := context.WithCancel(ctx)
	defer cancel()

	mgr := &Manager{}
	promptCtx := mgr.attachPromptCancel(parent, "sess-1")
	done := make(chan error, 1)
	go func() {
		<-promptCtx.Done()
		done <- promptCtx.Err()
	}()

	mgr.CancelInFlightPrompt("sess-1")
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
