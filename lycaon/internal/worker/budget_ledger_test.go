package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

type budgetFixture struct {
	db       *db.Store
	sessions *sessionstore.SQL
	queue    *worker.SQLQueue
	ledger   *worker.SQLBudgetLedger
	reg      *tools.DefaultRegistry
	notified []api.WorkerTask
}

// newBudgetFixture seeds a running worker job-1 on child-1 under parent-1.
func newBudgetFixture(t *testing.T, maxToolLoops int, budget spawn.WorkerToolBudget) *budgetFixture {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "parent-1", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "child-1", testdbseed.DefaultProjectID)
	_, err := database.ExecContext(t.Context(), `
		INSERT INTO worker_jobs(id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, max_tool_loops, tool_loops_used, created_at)
		VALUES ('job-1', ?, 'parent-1', 'child-1', 'security-reviewer', 'running', 'fixture', 'fixture', ?, 16, ?)`,
		testdbseed.DefaultProjectID, maxToolLoops, time.Now().UTC().Format(time.RFC3339Nano))
	testutil.FailErr(t, "insert worker job", err)
	f := &budgetFixture{db: database, sessions: sessionstore.NewSQL(database), queue: worker.NewSQLQueue(database, 1), reg: tools.NewDefaultRegistry()}
	outbox := eventoutbox.New(database, nil)
	f.sessions.SetEventOutbox(outbox)
	f.queue.SetEventOutbox(outbox)
	f.ledger = worker.NewSQLBudgetLedger(f.sessions, f.queue)
	toolBudget := func(string) spawn.WorkerToolBudget { return budget }
	testutil.FailErr(t, "register request_budget", worker.RegisterRequestBudgetTool(f.reg, worker.RequestBudgetToolDeps{
		Queue: f.queue, Ledger: f.ledger, ToolBudget: toolBudget,
		Notify: func(_ context.Context, task api.WorkerTask) { f.notified = append(f.notified, task) },
	}))
	testutil.FailErr(t, "register extend_worker_budget", worker.RegisterExtendWorkerBudgetTool(f.reg, worker.ExtendBudgetToolDeps{
		Queue: f.queue, Ledger: f.ledger, ToolBudget: toolBudget,
	}))
	testutil.FailErr(t, "register decline_worker_budget", worker.RegisterDeclineWorkerBudgetTool(f.reg, worker.DeclineBudgetToolDeps{
		Queue: f.queue, Ledger: f.ledger,
	}))
	return f
}

func (f *budgetFixture) decline(t *testing.T, sessionID string) (string, error) {
	t.Helper()
	return f.reg.Run(t.Context(), "decline_worker_budget", map[string]any{"job_id": "job-1"}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: sessionID},
	})
}

func (f *budgetFixture) request(t *testing.T, rounds int) (string, error) {
	t.Helper()
	return f.reg.Run(t.Context(), worker.RequestBudgetTool, map[string]any{
		"rounds": rounds, "remaining_work": []any{"trace the remaining spawn sites"},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "child-1",
			ParentSessionID: "parent-1",
			WorkerJobID:     "job-1"},
	})
}

func (f *budgetFixture) extend(t *testing.T, max int) (string, error) {
	t.Helper()
	return f.reg.Run(t.Context(), "extend_worker_budget", map[string]any{
		"job_id": "job-1", "max_tool_loops": max,
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "parent-1"},
	})
}

func (f *budgetFixture) job(t *testing.T) *api.WorkerTask {
	t.Helper()
	job, ok := f.queue.Get("job-1")
	if !ok {
		t.Fatal("job-1 missing")
	}
	return job
}

// A worker's ask is durable on its job, wakes its coordinator once, and a
// coordinator grant raises both ceilings and closes the request.
func TestWorkerAsksAndCoordinatorGrants(t *testing.T) {
	f := newBudgetFixture(t, 20, spawn.WorkerToolBudget{Default: 20, Min: 2, Max: 120})

	out, err := f.request(t, 12)
	testutil.FailErr(t, "request budget", err)
	var receipt map[string]any
	testutil.FailErr(t, "decode receipt", json.Unmarshal([]byte(out), &receipt))
	if receipt["requested_max"] != float64(32) || receipt["max_tool_loops"] != float64(20) {
		t.Fatalf("receipt = %v", receipt)
	}
	req := f.job(t).BudgetRequest
	if req == nil || req.RequestedMax != 32 || req.Rounds != 12 || req.ToolLoopsUsed != 16 || len(req.RemainingWork) != 1 {
		t.Fatalf("stored request = %+v", req)
	}
	if len(f.notified) != 1 || f.notified[0].BudgetRequest == nil {
		t.Fatalf("coordinator notices = %+v want one carrying the request", f.notified)
	}

	_, err = f.request(t, 4)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "WORKER_BUDGET_REQUEST_OPEN" {
		t.Fatalf("second request err = %v want WORKER_BUDGET_REQUEST_OPEN", err)
	}
	if len(f.notified) != 1 {
		t.Fatalf("a refused request must not wake the coordinator again: %d notices", len(f.notified))
	}

	_, err = f.extend(t, 32)
	testutil.FailErr(t, "grant", err)
	job := f.job(t)
	if job.MaxToolLoops != 32 || job.BudgetRequest != nil {
		t.Fatalf("job after grant = max %d request %+v", job.MaxToolLoops, job.BudgetRequest)
	}
	child, err := f.sessions.Get(t.Context(), "child-1")
	testutil.FailErr(t, "get child", err)
	if child.MaxToolLoops != 32 {
		t.Fatalf("child max = %d want 32", child.MaxToolLoops)
	}

	_, err = f.request(t, 8)
	testutil.FailErr(t, "a granted worker may ask again", err)
}

func TestRequestBoundsAskAtHostMaximum(t *testing.T) {
	f := newBudgetFixture(t, 110, spawn.WorkerToolBudget{Default: 20, Min: 2, Max: 120})
	_, err := f.request(t, 30)
	testutil.FailErr(t, "request", err)
	if req := f.job(t).BudgetRequest; req == nil || req.RequestedMax != 120 {
		t.Fatalf("request = %+v want requested_max bounded to 120", req)
	}
}

func TestRequestAtHostMaximumRejects(t *testing.T) {
	f := newBudgetFixture(t, 120, spawn.WorkerToolBudget{Default: 20, Min: 2, Max: 120})
	_, err := f.request(t, 10)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "WORKER_BUDGET_REQUEST_AT_HOST_MAX" {
		t.Fatalf("err = %v want WORKER_BUDGET_REQUEST_AT_HOST_MAX", err)
	}
	if len(f.notified) != 0 || f.job(t).BudgetRequest != nil {
		t.Fatal("an ungrantable request must not be recorded or wake the coordinator")
	}
}

func TestRequestOutsideAWorkerLegRejects(t *testing.T) {
	f := newBudgetFixture(t, 20, spawn.WorkerToolBudget{Default: 20, Min: 2, Max: 120})
	_, err := f.reg.Run(t.Context(), worker.RequestBudgetTool, map[string]any{
		"rounds": 4, "remaining_work": []any{"more"},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "parent-1"},
	})
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "REQUEST_BUDGET_ADDRESSED_SESSION" {
		t.Fatalf("err = %v want REQUEST_BUDGET_ADDRESSED_SESSION", err)
	}
}

func TestGrantRejectsTerminalJob(t *testing.T) {
	f := newBudgetFixture(t, 20, spawn.WorkerToolBudget{Default: 20, Min: 2, Max: 120})
	_, err := f.db.ExecContext(t.Context(), `UPDATE worker_jobs SET status = 'complete' WHERE id = 'job-1'`)
	testutil.FailErr(t, "settle job", err)
	_, err = f.extend(t, 40)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "WORKER_BUDGET_EXTEND_NOT_RUNNING" {
		t.Fatalf("err = %v want WORKER_BUDGET_EXTEND_NOT_RUNNING", err)
	}
	if err := f.ledger.Grant(t.Context(), "child-1", "job-1", 40); !errors.Is(err, worker.ErrWorkerBudgetNotLive) {
		t.Fatalf("ledger grant on a settled job = %v want ErrWorkerBudgetNotLive", err)
	}
}

func TestGrantRejectsNonIncrease(t *testing.T) {
	f := newBudgetFixture(t, 40, spawn.WorkerToolBudget{Default: 20, Min: 2, Max: 120})
	_, err := f.extend(t, 30)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "WORKER_BUDGET_EXTEND_NOT_INCREASE" {
		t.Fatalf("err = %v want WORKER_BUDGET_EXTEND_NOT_INCREASE", err)
	}
}

func TestGrantRollsBackBothCeilings(t *testing.T) {
	f := newBudgetFixture(t, 40, spawn.WorkerToolBudget{Default: 20, Min: 2, Max: 120})
	_, err := f.db.ExecContext(t.Context(), `
		CREATE TRIGGER reject_budget_update BEFORE UPDATE OF max_tool_loops ON worker_jobs
		WHEN NEW.max_tool_loops = 80
		BEGIN SELECT RAISE(ABORT, 'reject budget update'); END`)
	testutil.FailErr(t, "create rejecting trigger", err)
	if err := f.ledger.Grant(t.Context(), "child-1", "job-1", 80); err == nil {
		t.Fatal("grant succeeded despite job failure")
	}
	child, err := f.sessions.Get(t.Context(), "child-1")
	testutil.FailErr(t, "get child", err)
	if child.MaxToolLoops != 0 {
		t.Fatalf("child max = %d want 0", child.MaxToolLoops)
	}
	if f.job(t).MaxToolLoops != 40 {
		t.Fatalf("job max = %d want 40", f.job(t).MaxToolLoops)
	}
}

// A decline closes the request and leaves the ceiling, so the worker may
// still finish within it and a resume inherits nothing.
func TestCoordinatorDeclinesAnOpenRequest(t *testing.T) {
	f := newBudgetFixture(t, 20, spawn.WorkerToolBudget{Default: 20, Min: 2, Max: 120})
	_, err := f.decline(t, "parent-1")
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "WORKER_BUDGET_DECLINE_NO_REQUEST" {
		t.Fatalf("decline without a request err = %v want WORKER_BUDGET_DECLINE_NO_REQUEST", err)
	}
	_, err = f.request(t, 6)
	testutil.FailErr(t, "request budget", err)
	_, err = f.decline(t, "other-parent")
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "WORKER_BUDGET_DECLINE_SESSION_MISMATCH" {
		t.Fatalf("foreign decline err = %v want WORKER_BUDGET_DECLINE_SESSION_MISMATCH", err)
	}
	if f.job(t).BudgetRequest == nil {
		t.Fatal("a refused decline closed the request")
	}

	out, err := f.decline(t, "parent-1")
	testutil.FailErr(t, "decline", err)
	var receipt map[string]any
	testutil.FailErr(t, "decode receipt", json.Unmarshal([]byte(out), &receipt))
	if receipt["max_tool_loops"] != float64(20) || receipt["requested_max"] != float64(26) {
		t.Fatalf("receipt = %v", receipt)
	}
	job := f.job(t)
	if job.MaxToolLoops != 20 || job.BudgetRequest != nil {
		t.Fatalf("job after decline = max %d request %+v, want the ceiling kept and the request closed", job.MaxToolLoops, job.BudgetRequest)
	}
	_, err = f.decline(t, "parent-1")
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "WORKER_BUDGET_DECLINE_NO_REQUEST" {
		t.Fatalf("second decline err = %v want WORKER_BUDGET_DECLINE_NO_REQUEST", err)
	}
}
