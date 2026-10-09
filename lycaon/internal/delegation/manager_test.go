package delegation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

type allowWorkflowRuns struct{}

func (allowWorkflowRuns) AssertRunnable(context.Context, string) error { return nil }

func (allowWorkflowRuns) AssertWorkerTask(context.Context, *api.WorkerTask) error { return nil }

func delegationTestMockConfig(t *testing.T) *llm.MockConfig {
	t.Helper()
	cfg, err := llm.LoadMockConfig()
	if err != nil {
		t.Fatalf("load mock config: %v", err)
	}
	return cfg
}

func newDelegationTestManager(t *testing.T) (*session.Manager, *MemoryStore, *worker.InMemoryQueue, *Manager, project.Registry) {
	t.Helper()
	store := store.NewMemory()
	mgr := session.NewManager(store, llm.NewMockProvider(delegationTestMockConfig(t)), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	delegationStore := NewMemoryStore()
	queue := worker.NewInMemoryQueue(10)
	reg := project.NewMemoryRegistry()
	delegationMgr := NewManager(delegationStore, queue, mgr, AllowGate{})
	delegationMgr.Projects = reg
	return mgr, delegationStore, queue, delegationMgr, reg
}

func seedDelegationProject(t *testing.T, reg project.Registry, dir string) string {
	t.Helper()
	p, err := project.CreateWithRoot(context.Background(), reg, dir)
	testutil.FailErr(t, "project.CreateWithRoot failed", err)
	return p.ID
}

func TestDispatchLegAllowsNoFocusPaths(t *testing.T) {
	_, _, queue, delegationMgr, reg := newDelegationTestManager(t)
	ctx := context.Background()
	projectID := seedDelegationProject(t, reg, t.TempDir())
	delegation, err := delegationMgr.Create(ctx, api.CreateDelegationRequest{
		ProjectID: projectID,
		Task:      "implement feature",
	})
	testutil.FailErr(t, "Create", err)
	leg := &delegation.Legs[0]
	leg.Files = nil
	testutil.FailErr(t, "UpdateLeg", delegationMgr.Store.UpdateLeg(ctx, *leg))

	dispatched, err := delegationMgr.DispatchLeg(ctx, delegation.ID, leg.ID, "")
	testutil.FailErr(t, "DispatchLeg", err)
	task, ok := queue.Get(dispatched.WorkerID)
	if !ok || task == nil {
		t.Fatal("dispatched worker task missing")
	}
	if got := task.EffectiveScope().Paths; len(got) != 0 {
		t.Fatalf("focus paths = %v want none", got)
	}
}

func TestDelegationSingleLegDispatchAndCloseout(t *testing.T) {
	mgr, _, queue, delegationMgr, reg := newDelegationTestManager(t)
	ctx := context.Background()
	dir := t.TempDir()
	projectID := seedDelegationProject(t, reg, dir)
	r, err := delegationMgr.Create(ctx, api.CreateDelegationRequest{
		ProjectID: projectID,
		Task:      "implement feature",
		Strategy:  api.HuntStrategyFileBased,
	})
	testutil.FailErr(t, "delegationMgr.Create failed", err)
	if len(r.Legs) != 1 {
		t.Fatalf("legs = %d", len(r.Legs))
	}
	legID := r.Legs[0].ID

	leg, err := delegationMgr.DispatchLeg(ctx, r.ID, legID, "")
	testutil.FailErr(t, "delegationMgr.DispatchLeg failed", err)
	if leg.Status != api.LegStatusDispatched {
		t.Fatalf("status = %q", leg.Status)
	}
	task, ok := queue.Get(leg.WorkerID)
	if !ok || task == nil {
		t.Fatal("dispatched leg missing worker task")
	}
	if !task.EffectiveScope().IsWrite() || strings.TrimSpace(task.OverlayID) == "" {
		t.Fatalf("implementer dispatch scope=%+v overlay_id=%q, want write overlay", task.EffectiveScope(), task.OverlayID)
	}

	exec := worker.NewLocalWorkerExecutor(mgr, queue)
	exec.SetPromptInjects(promptstest.InjectRenderer(t))
	poller := worker.NewLocalWorkerPoller(queue, exec, worker.DefaultWorkersConfig(), &worker.SessionOutcomeBridge{Inner: delegationMgr})
	queue.SetRunningCancel(poller.Abort)
	pollCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	go poller.Run(pollCtx)

	testutil.WaitFor(t, 5*time.Second, func() bool {
		st, err := delegationMgr.GetStatus(ctx, r.ID)
		testutil.FailErr(t, "delegationMgr.GetStatus failed", err)
		return st.Phase == api.DelegationPhaseDone
	})
}

func TestDelegationAbort(t *testing.T) {
	_, _, _, delegationMgr, reg := newDelegationTestManager(t)
	ctx := context.Background()
	dir := t.TempDir()
	projectID := seedDelegationProject(t, reg, dir)
	r, err := delegationMgr.Create(ctx, api.CreateDelegationRequest{
		ProjectID: projectID,
		Task:      "task",
		Strategy:  api.HuntStrategyFileBased,
	})
	testutil.FailErr(t, "delegationMgr.Create failed", err)
	legID := r.Legs[0].ID
	if _, err := delegationMgr.DispatchLeg(ctx, r.ID, legID, ""); err != nil {
		testutil.FailErr(t, "delegationMgr.DispatchLeg failed", err)
	}
	if err := delegationMgr.Abort(ctx, r.ID, "user_canceled"); err != nil {
		testutil.FailErr(t, "delegationMgr.Abort failed", err)
	}
	st, _ := delegationMgr.GetStatus(ctx, r.ID)
	if st.Status != "aborted" {
		t.Fatalf("status = %q", st.Status)
	}
	if st.Reason != "user_canceled" {
		t.Fatalf("reason = %q, want user_canceled", st.Reason)
	}
	for _, leg := range st.Legs {
		if leg.Status != api.LegStatusCanceled || leg.CompletedAt == nil {
			t.Fatalf("aborted leg = %s completed_at=%v, want canceled and settled", leg.Status, leg.CompletedAt)
		}
	}
}

func TestCreateReplaysItsOperation(t *testing.T) {
	_, store, _, delegationMgr, reg := newDelegationTestManager(t)
	projectID := seedDelegationProject(t, reg, t.TempDir())
	req := api.CreateDelegationRequest{OperationID: "op-1", ProjectID: projectID, Task: "implement"}
	first, err := delegationMgr.Create(t.Context(), req)
	testutil.FailErr(t, "first create", err)
	if first.Strategy != api.HuntStrategyFileBased {
		t.Fatalf("strategy = %q, want the file-based default", first.Strategy)
	}

	req.Strategy = api.HuntStrategyFileBased
	replayed, err := delegationMgr.Create(t.Context(), req)
	testutil.FailErr(t, "replayed create", err)
	if replayed.ID != first.ID || replayed.CoordinatorSessionID != first.CoordinatorSessionID {
		t.Fatalf("replay = %s/%s, want %s/%s", replayed.ID, replayed.CoordinatorSessionID, first.ID, first.CoordinatorSessionID)
	}
	all, err := store.ListByProject(t.Context(), projectID, "")
	testutil.FailErr(t, "list delegations", err)
	if len(all) != 1 {
		t.Fatalf("delegations = %d, want 1", len(all))
	}

	req.Task = "something else"
	if _, err := delegationMgr.Create(t.Context(), req); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("create with a different request = %v, want ErrOperationConflict", err)
	}
}

func TestRecordOutcomeDoesNotMarkIncompleteWorkComplete(t *testing.T) {
	_, store, queue, manager, _ := newDelegationTestManager(t)
	ctx := t.Context()
	created, err := store.Create(ctx, api.Delegation{ProjectID: "project"}, "session", []api.Leg{
		{ID: "partial", Status: api.LegStatusRunning, WorkerID: "job-partial"},
		{ID: "held", Status: api.LegStatusRunning, WorkerID: "job-held"},
	})
	testutil.FailErr(t, "create delegation", err)
	for _, tc := range []struct {
		legID  string
		status string
		want   api.LegStatus
	}{
		{legID: "partial", status: "partial", want: api.LegStatusFailed},
		{legID: "held", status: "needs_decision", want: api.LegStatusHeld},
	} {
		t.Run(tc.status, func(t *testing.T) {
			testutil.FailErr(t, "record outcome", manager.RecordOutcome(ctx, created.ID, tc.legID, "job-"+tc.legID, api.WorkerResult{Status: tc.status}))
			leg, err := store.GetLeg(ctx, created.ID, tc.legID)
			testutil.FailErr(t, "get leg", err)
			if leg.Status != tc.want {
				t.Fatalf("leg status = %q, want %q", leg.Status, tc.want)
			}
		})
	}
	if _, ok := queue.Get("job-partial"); ok {
		t.Fatal("outcome recording should not enqueue a worker")
	}
}

func TestBudgetExhaustedLegResumesPreservedChild(t *testing.T) {
	_, store, queue, manager, reg := newDelegationTestManager(t)
	ctx := t.Context()
	projectID := seedDelegationProject(t, reg, t.TempDir())
	queue.SetWorkflowDomains(&worker.WorkflowDomains{Runs: allowWorkflowRuns{}, Tasks: allowWorkflowRuns{}})
	delegation, err := manager.Create(ctx, api.CreateDelegationRequest{
		ProjectID: projectID, Task: "inspect the implementation", WorkflowRunID: "workflow-run",
	})
	testutil.FailErr(t, "create delegation", err)
	leg := delegation.Legs[0]
	dispatched, err := manager.DispatchLeg(ctx, delegation.ID, leg.ID, "")
	testutil.FailErr(t, "dispatch leg", err)
	prior, ok := queue.Get(dispatched.WorkerID)
	if !ok || prior == nil {
		t.Fatal("initial worker task missing")
	}
	testutil.FailErr(t, "bind child", queue.SetChildSessionID(ctx, prior.ID, "child-session"))
	testutil.FailErr(t, "record budget exhaustion", manager.RecordOutcome(ctx, delegation.ID, leg.ID, dispatched.WorkerID, api.WorkerResult{
		Status: "partial", HintCode: session.WorkerBudgetExhaustedCode, Summary: "survey is incomplete",
	}))
	pending, err := store.GetLeg(ctx, delegation.ID, leg.ID)
	testutil.FailErr(t, "load retry pending leg", err)
	if pending.Status != api.LegStatusRetryPending || pending.CompletedAt != nil {
		t.Fatalf("retry pending leg = %+v", pending)
	}

	resumed, err := manager.ResumeLeg(ctx, delegation.ID, leg.ID)
	testutil.FailErr(t, "resume leg", err)
	next, ok := queue.Get(resumed.WorkerID)
	if !ok || next == nil {
		t.Fatal("continuation worker task missing")
	}
	if next.ChildSessionID != "child-session" || next.SpawnReason != api.SpawnReasonRetry {
		t.Fatalf("continuation identity = %+v", next)
	}
	if next.MaxToolLoops <= prior.MaxToolLoops {
		t.Fatalf("continuation max_tool_loops = %d want > %d", next.MaxToolLoops, prior.MaxToolLoops)
	}
	if resumed.Status != api.LegStatusDispatched || resumed.Result != nil {
		t.Fatalf("resumed leg = %+v", resumed)
	}
}

type abortFailureQueue struct {
	worker.WorkerQueue
	listErr   error
	cancelErr error
}

func (q abortFailureQueue) List(ctx context.Context, projectID string, status ...api.WorkerStatus) ([]api.WorkerTask, error) {
	if q.listErr != nil {
		return nil, q.listErr
	}
	return q.WorkerQueue.List(ctx, projectID, status...)
}

func (q abortFailureQueue) Cancel(ctx context.Context, id string, result *api.WorkerResult) error {
	if q.cancelErr != nil {
		return q.cancelErr
	}
	return q.WorkerQueue.Cancel(ctx, id, result)
}

func TestDelegationAbortDoesNotSettleWhenWorkersCannotBeCanceled(t *testing.T) {
	for _, tc := range []struct {
		name      string
		listErr   error
		cancelErr error
	}{
		{name: "list failure", listErr: errors.New("queue unavailable")},
		{name: "cancel failure", cancelErr: errors.New("cancel unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, queue, delegationMgr, reg := newDelegationTestManager(t)
			ctx := t.Context()
			projectID := seedDelegationProject(t, reg, t.TempDir())
			delegation, err := delegationMgr.Create(ctx, api.CreateDelegationRequest{
				ProjectID: projectID, Task: "task", Strategy: api.HuntStrategyFileBased,
			})
			testutil.FailErr(t, "create delegation", err)
			_, err = delegationMgr.DispatchLeg(ctx, delegation.ID, delegation.Legs[0].ID, "")
			testutil.FailErr(t, "dispatch leg", err)

			delegationMgr.Queue = abortFailureQueue{WorkerQueue: queue, listErr: tc.listErr, cancelErr: tc.cancelErr}
			if err := delegationMgr.Abort(ctx, delegation.ID, "test_abort"); err == nil {
				t.Fatal("abort succeeded without canceling every worker")
			}
			status, err := delegationMgr.GetStatus(ctx, delegation.ID)
			testutil.FailErr(t, "get delegation after abort failure", err)
			if status.Status == "aborted" || status.Phase == api.DelegationPhaseDone {
				t.Fatalf("delegation settled after abort failure: status=%q phase=%q", status.Status, status.Phase)
			}
		})
	}
}
