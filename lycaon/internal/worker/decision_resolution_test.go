package worker

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSQLDecisionResolverRollsBackEveryWrite(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "parent-1", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "child-1", testdbseed.DefaultProjectID)
	_, err := database.ExecContext(t.Context(), `
		INSERT INTO worker_jobs(id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at)
		VALUES (?, ?, ?, ?, 'implementer', 'held', 'fixture', 'fixture', ?)`,
		"job-1", testdbseed.DefaultProjectID, "parent-1", "child-1", time.Now().UTC().Format(time.RFC3339Nano))
	testutil.FailErr(t, "insert worker job", err)
	decisions := sessiondecisions.NewSQL(database)
	testutil.FailErr(t, "put decision", decisions.Put(t.Context(), api.WorkerDecisionRequest{
		ChildSessionID: "child-1", WorkerID: "job-1", Question: "Choose one", Options: []string{"A", "B"},
	}))
	_, err = database.ExecContext(t.Context(), `
		CREATE TRIGGER reject_decision_message BEFORE INSERT ON messages
		BEGIN SELECT RAISE(ABORT, 'reject decision message'); END`)
	testutil.FailErr(t, "create rejecting trigger", err)

	messages := sessionstore.NewSQL(database)
	outbox := eventoutbox.New(database, nil)
	messages.SetEventOutbox(outbox)
	queue := NewSQLQueue(database, 1)
	queue.SetEventOutbox(outbox)
	resolver := NewSQLDecisionResolver(messages, queue)
	decision, ok, err := decisions.Get(t.Context(), "child-1")
	testutil.FailErr(t, "get decision", err)
	if !ok {
		t.Fatal("decision missing before resolution")
	}
	err = resolver.Resolve(t.Context(), decision, api.Message{
		Role: api.MessageRoleUser, Content: "Decision: A",
	})
	if err == nil {
		t.Fatal("resolution succeeded despite transcript failure")
	}
	var jobs int
	testutil.FailErr(t, "count jobs", database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM worker_jobs`).Scan(&jobs))
	if jobs != 1 {
		t.Fatalf("worker jobs = %d want 1", jobs)
	}
	if _, ok, getErr := decisions.Get(t.Context(), "child-1"); getErr != nil || !ok {
		t.Fatalf("decision after rollback: ok=%v err=%v", ok, getErr)
	}
}

func TestSQLDecisionResolverRequeuesSameWorkerWithBranchContinuity(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "parent-1", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "child-1", testdbseed.DefaultProjectID)

	queue := NewSQLQueue(database, 1)
	store := queue.store
	testutil.FailErr(t, "insert worker", store.InsertTask(t.Context(), api.WorkerTask{
		ID: "job-1", ProjectID: testdbseed.DefaultProjectID,
		ParentSessionID: "parent-1", ChildSessionID: "child-1",
		AgentType: "implementer", Status: api.WorkerStatusHeld,
		ExecutionTarget: api.ExecutionTargetLocal, Prompt: "fixture", Brief: "fixture",
		OverlayID: "job-1", MaxToolLoops: 14,
	}))
	workspaceRoot := filepath.Join(testbaseline.DataDir(t, database), "worker-branches", "job-1")
	baseline := testbaseline.Durable(t, database, "job-1", t.TempDir())
	bound, err := store.SetWorkerWorkspace(t.Context(), "job-1", workspaceRoot, baseline)
	testutil.FailErr(t, "bind worker workspace", err)
	if !bound {
		t.Fatal("workspace bind lost unexpectedly")
	}
	_, err = database.ExecContext(t.Context(), `
		UPDATE worker_jobs SET tool_loops_used = 5, tool_calls_used = 17,
		result_json = '{"summary":"needs a choice"}', completed_at = ? WHERE id = 'job-1'`,
		time.Now().UTC().Format(time.RFC3339Nano))
	testutil.FailErr(t, "seed durable worker progress", err)

	decisions := sessiondecisions.NewSQL(database)
	decision := api.WorkerDecisionRequest{
		ChildSessionID: "child-1", WorkerID: "job-1", Question: "Choose one", Options: []string{"A", "B"},
	}
	testutil.FailErr(t, "put decision", decisions.Put(t.Context(), decision))
	decision, ok, err := decisions.Get(t.Context(), "child-1")
	testutil.FailErr(t, "reload normalized decision", err)
	if !ok {
		t.Fatal("normalized decision missing")
	}
	messages := sessionstore.NewSQL(database)
	outbox := eventoutbox.New(database, nil)
	messages.SetEventOutbox(outbox)
	queue.SetEventOutbox(outbox)
	testutil.FailErr(t, "ack decision checkpoint", db.New(database).MarkWorkerOutcomeDelivered(
		t.Context(), db.MarkWorkerOutcomeDeliveredParams{
			WorkerJobID: "job-1", DeliveredAt: db.FormatTime(time.Now().UTC()),
		},
	))

	err = NewSQLDecisionResolver(messages, queue).Resolve(t.Context(), decision, api.Message{
		Role: api.MessageRoleUser, Content: "Decision: A",
	})
	testutil.FailErr(t, "resolve decision", err)
	task, ok := queue.Get("job-1")
	if !ok || task == nil {
		t.Fatal("resumed worker missing")
	}
	if task.Status != api.WorkerStatusPending || task.ChildSessionID != "child-1" || task.OverlayID != "job-1" {
		t.Fatalf("resumed worker identity = %+v", task)
	}
	if task.WorkspaceRoot != workspaceRoot || task.WorkspaceBaselinePath != baseline {
		t.Fatalf("resumed branch = root %q baseline %q", task.WorkspaceRoot, task.WorkspaceBaselinePath)
	}
	if task.ToolLoopsUsed != 5 || task.ToolCallsUsed != 17 || task.MaxToolLoops != 14 {
		t.Fatalf("resumed counters = loops %d calls %d max %d", task.ToolLoopsUsed, task.ToolCallsUsed, task.MaxToolLoops)
	}
	if task.Result != nil || task.CompletedAt != nil {
		t.Fatalf("completed state survived resume: result=%+v completed=%v", task.Result, task.CompletedAt)
	}
	var jobs int
	testutil.FailErr(t, "count jobs", database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM worker_jobs`).Scan(&jobs))
	if jobs != 1 {
		t.Fatalf("worker jobs = %d want 1", jobs)
	}
	if _, ok, getErr := decisions.Get(t.Context(), "child-1"); getErr != nil || ok {
		t.Fatalf("decision after resolution: ok=%v err=%v", ok, getErr)
	}
	var delivered int
	testutil.FailErr(t, "count stale checkpoint deliveries", database.QueryRowContext(
		t.Context(), `SELECT COUNT(*) FROM worker_outcome_deliveries WHERE worker_job_id = 'job-1'`,
	).Scan(&delivered))
	if delivered != 0 {
		t.Fatalf("stale checkpoint deliveries = %d want 0", delivered)
	}
}
