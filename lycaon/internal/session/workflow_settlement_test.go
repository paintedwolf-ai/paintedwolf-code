package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type settlementWorkflowSource struct {
	active *api.WorkflowRun
	err    error
}

func (s *settlementWorkflowSource) ActiveBySession(context.Context, string) (*api.WorkflowRun, error) {
	return s.active, s.err
}
func (*settlementWorkflowSource) GetScaffoldVars(context.Context, string) (map[string]any, error) {
	return nil, nil
}
func (*settlementWorkflowSource) HumanApprovalAwaiting(context.Context, string) (bool, error) {
	return false, nil
}
func (*settlementWorkflowSource) HostObligationHeld(context.Context, string) (bool, error) {
	return false, nil
}
func (*settlementWorkflowSource) HostObligationHoldKinds(context.Context, string) []string {
	return nil
}

type settlementWorkerQueue struct {
	noopWorkerBranchClaim
	idle atomic.Bool
}

func (q *settlementWorkerQueue) ListBySession(_ context.Context, projectID, sessionID string, _ ...api.WorkerStatus) ([]api.WorkerTask, error) {
	if q.idle.Load() {
		return nil, nil
	}
	return []api.WorkerTask{{ID: "waiting-worker", ProjectID: projectID, ParentSessionID: sessionID, Status: api.WorkerStatusRunning}}, nil
}
func (*settlementWorkerQueue) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) {
	return nil, nil
}
func (*settlementWorkerQueue) Get(string) (*api.WorkerTask, bool) { return nil, false }

func TestWorkflowCompletionSettlesAfterExecutionDrains(t *testing.T) {
	for _, blocker := range []string{"none", "execution", "workers", "session lock"} {
		t.Run(blocker, func(t *testing.T) {
			ctx := t.Context()
			st := store.NewMemory()
			mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
			loopWorkflowFixture1 := &settlementWorkflowSource{}
			mgr.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: loopWorkflowFixture1, Approvals: loopWorkflowFixture1, Obligations: loopWorkflowFixture1})
			sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			testutil.FailErr(t, "mark busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))
			hub := events.NewMemoryHub()
			mgr.SetEventPublisher(&events.Publisher{Hub: hub})
			eventCh, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
			testutil.FailErr(t, "subscribe", err)
			t.Cleanup(unsubscribe)
			loop := mgr.ensureCoordinatorRuntime().CoordinatorLoop()
			workers := &settlementWorkerQueue{}
			workers.idle.Store(blocker != "workers")
			mgr.workerQueue = workers
			loop.EnterSleep(ctx, sess.ID, time.Now().Add(time.Hour), "phase wait", loopwake.HostObligationWaitTriggers(false), nil, loopwake.SleepMoverHost)
			unblock := func() {}
			switch blocker {
			case "execution":
				unblock = loop.BeginPromptExecution(ctx, sess.ID)
			case "workers":
				unblock = func() { workers.idle.Store(true) }
			case "session lock":
				lock := mgr.promptState.Prompt.Acquire(sess.ID)
				lock.Lock()
				unblock = lock.Unlock
			}
			testutil.FailErr(t, "notify workflow completion", mgr.SettleCompletedWorkflow(ctx, sess.ID, "finished-run"))
			if blocker != "none" {
				current, err := st.Get(ctx, sess.ID)
				testutil.FailErr(t, "read held session", err)
				if current.Status != api.SessionStatusBusy {
					t.Fatalf("settled while %s remained", blocker)
				}
				assertNoSessionIdleEvent(t, eventCh)
			}
			unblock()
			testutil.FailErr(t, "settle after drain", mgr.settleDeferredUserTurn(ctx, sess.ID))
			if disposition := awaitIdleDisposition(t, eventCh); disposition != api.SessionIdleDispositionCompleted {
				t.Fatalf("disposition=%q", disposition)
			}
			current, err := st.Get(ctx, sess.ID)
			testutil.FailErr(t, "read settled session", err)
			if current.Status != api.SessionStatusIdle || loop.IsSleeping(sess.ID) {
				t.Fatalf("status=%q sleeping=%v", current.Status, loop.IsSleeping(sess.ID))
			}
			testutil.FailErr(t, "replay completed notification", mgr.SettleCompletedWorkflow(ctx, sess.ID, "finished-run"))
			assertNoSessionIdleEvent(t, eventCh)
			if _, ok := mgr.deferredWorkflowCompletions.Load(sess.ID); ok {
				t.Fatal("completed notification retained after settlement")
			}
		})
	}
}

func TestWorkflowCompletionDoesNotSettleAnotherActiveRun(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemory()
	mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	source := &settlementWorkflowSource{active: &api.WorkflowRun{ID: "new-run", Status: api.WorkflowRunStatusRunning}}
	loopWorkflowFixture2 := source
	mgr.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: loopWorkflowFixture2, Approvals: loopWorkflowFixture2, Obligations: loopWorkflowFixture2})
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))
	testutil.FailErr(t, "deliver stale completion", mgr.SettleCompletedWorkflow(ctx, sess.ID, "old-run"))
	current, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "read current session", err)
	if current.Status != api.SessionStatusBusy {
		t.Fatal("old workflow settled the active run")
	}
	if _, ok := mgr.deferredWorkflowCompletions.Load(sess.ID); ok {
		t.Fatal("stale notification retained")
	}
}

func TestWorkflowCompletionRetriesLookupFailure(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemory()
	mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	want := errors.New("workflow store unavailable")
	source := &settlementWorkflowSource{err: want}
	loopWorkflowFixture3 := source
	mgr.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: loopWorkflowFixture3, Approvals: loopWorkflowFixture3, Obligations: loopWorkflowFixture3})
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))
	if err := mgr.SettleCompletedWorkflow(ctx, sess.ID, "finished-run"); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
	current, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "read current session", err)
	if current.Status != api.SessionStatusBusy {
		t.Fatal("lookup failure was treated as idle")
	}
	source.err = nil
	testutil.FailErr(t, "retry settlement", mgr.settleDeferredUserTurn(ctx, sess.ID))
	current, err = st.Get(ctx, sess.ID)
	testutil.FailErr(t, "read recovered session", err)
	if current.Status != api.SessionStatusIdle {
		t.Fatalf("status=%q", current.Status)
	}
}
