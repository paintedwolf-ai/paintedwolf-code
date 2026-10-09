package workflow

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/hostctx"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"sync"
	"testing"
	"time"
)

func TestFollowUpRepairsPersistedWorkflowGap(t *testing.T) {
	mgr, sessions, _, _ := testManagerWithRegistry(t)
	original, err := mgr.Ambient.StartAmbient(t.Context(), "sess-1", "implement", "1.0.0")
	testutil.FailErr(t, "start original workflow", err)
	original.Status = api.WorkflowRunStatusComplete
	completedAt := time.Now().UTC()
	original.CompletedAt = &completedAt
	testutil.FailErr(t, "persist completed workflow", mgr.Store.State.Update(t.Context(), original))
	// The new manager reads only persisted workflow state.
	recovered := NewManager(mgr.Store, sessions, mgr.Resolver.Overlay, nil)
	if !recovered.Requests.AcceptsEmptyRequest(t.Context(), "sess-1") {
		t.Fatal("empty follow-up should use the default request contract")
	}
	active, err := recovered.Store.Runs.ActiveBySession(t.Context(), "sess-1")
	testutil.FailErr(t, "read after preflight", err)
	if active != nil {
		t.Fatal("preflight created a workflow")
	}
	if !errors.Is(recovered.Policy.AssertSessionRunnable(t.Context(), "sess-1"), runstate.ErrNoActiveRun) {
		t.Fatal("unbound coordinator execution must be rejected")
	}
	_, response, handled, err := recovered.Requests.PrepareUserRequest(t.Context(), "sess-1", "")
	testutil.FailErr(t, "prepare empty follow-up", err)
	if !handled || response == nil {
		t.Fatal("empty follow-up must open the default workflow question")
	}
	active, err = recovered.Store.Runs.ActiveBySession(t.Context(), "sess-1")
	testutil.FailErr(t, "read recovered workflow", err)
	if active == nil || active.ID == original.ID || !runstate.IsAmbientRun(active) {
		t.Fatalf("recovered workflow = %+v", active)
	}
	old, err := recovered.Store.Runs.Get(t.Context(), original.ID)
	testutil.FailErr(t, "read original history", err)
	if old.Status != api.WorkflowRunStatusComplete {
		t.Fatal("follow-up rewrote completed history")
	}
}

func TestEnsureSessionWorkflowConcurrentRequestsShareRun(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	const count = 8
	runs := make([]*api.WorkflowRun, count)
	errs := make([]error, count)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range count {
		wg.Go(func() {
			<-start
			runs[i], errs[i] = mgr.Ambient.EnsureSessionWorkflow(t.Context(), "sess-1")
		})
	}
	close(start)
	wg.Wait()
	for i := range count {
		testutil.FailErr(t, "ensure concurrent workflow", errs[i])
		if runs[i] == nil || runs[0] == nil || runs[i].ID != runs[0].ID {
			t.Fatalf("concurrent requests selected different runs: %+v", runs)
		}
	}
}

type workflowSessionReadStub struct {
	session.Store
	sess *api.Session
	err  error
}

func (s workflowSessionReadStub) Get(context.Context, string) (*api.Session, error) {
	return s.sess, s.err
}

func TestEnsureSessionWorkflowWorkersAndReadFailures(t *testing.T) {
	mgr, sessions, _, _ := testManager(t)
	workerSessions := workflowSessionReadStub{Store: sessions, sess: &api.Session{ID: "worker", ParentSessionID: "sess-1"}}
	mgr.Policy.Sessions = workerSessions
	mgr.Ambient.Sessions = workerSessions
	run, err := mgr.Ambient.EnsureSessionWorkflow(t.Context(), "worker")
	testutil.FailErr(t, "worker execution scope", err)
	if run != nil {
		t.Fatal("worker received an independent ambient workflow")
	}
	testutil.FailErr(t, "worker runnable without ambient", mgr.Policy.AssertSessionRunnable(t.Context(), "worker"))
	failure := errors.New("session read failed")
	failedSessions := workflowSessionReadStub{Store: sessions, err: failure}
	mgr.Policy.Sessions = failedSessions
	mgr.Ambient.Sessions = failedSessions
	mgr.Requests.Sessions = failedSessions
	_, _, _, err = mgr.Requests.PrepareUserRequest(t.Context(), "sess-1", "follow up")
	if !errors.Is(err, failure) {
		t.Fatalf("prepare error = %v, want session read failure", err)
	}
	if mgr.Requests.AcceptsEmptyRequest(t.Context(), "sess-1") {
		t.Fatal("failed request preflight must deny")
	}
}

func TestAmbientAttachmentNeverReplacesActiveCatalogRun(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	run, err := startRun(t.Context(), mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "start catalog workflow", err)
	_, err = mgr.Ambient.StartAmbient(hostctx.WithHumanWorkflowStart(t.Context()), run.SessionID, "implement", "1.0.0")
	if !errors.Is(err, runstate.ErrActiveRunExists) {
		t.Fatalf("ambient attachment = %v, want active conflict", err)
	}
	active, err := mgr.Ambient.EnsureSessionWorkflow(t.Context(), run.SessionID)
	testutil.FailErr(t, "retain active catalog run", err)
	if active == nil || active.ID != run.ID {
		t.Fatalf("active catalog run replaced: %+v", active)
	}
}

func TestWorkflowRepairRespectsSessionStopAdmission(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	mgr.Starts.Barrier = rejectingSessionAdmission{}
	_, _, _, err := mgr.Requests.PrepareUserRequest(t.Context(), "sess-1", "follow up")
	if !errors.Is(err, lifecycle.ErrStopping) {
		t.Fatalf("prepare = %v, want stop admission rejection", err)
	}
	run, err := mgr.Store.Runs.ActiveBySession(t.Context(), "sess-1")
	testutil.FailErr(t, "read after rejected repair", err)
	if run != nil {
		t.Fatalf("rejected repair attached a workflow: %+v", run)
	}
}

type rejectingSessionAdmission struct{}

func (rejectingSessionAdmission) WithSessionTreeAdmission(context.Context, string, func() error) error {
	return lifecycle.ErrStopping
}
