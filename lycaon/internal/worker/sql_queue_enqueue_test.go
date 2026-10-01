package worker

import (
	"context"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestSQLQueueEnqueueReturnsJobID(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	testdbseed.InsertSession(t, sqlDB, "parent-1", testdbseed.DefaultProjectID)
	q := NewSQLQueue(sqlDB, 2)
	id, err := q.EnqueueWithProjectID(context.Background(), testdbseed.DefaultProjectID, api.WorkerTask{
		ParentSessionID: "parent-1",
		ProjectID:       testdbseed.DefaultProjectID,
		AgentType:       "implementer",
		Prompt:          "do work",
		Brief:           "Do work",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "q.EnqueueWithProjectID failed", err)
	if id == "" {
		t.Fatal("Enqueue returned empty job id")
	}
	select {
	case <-q.RunnableWake():
	default:
		t.Fatal("committed enqueue did not wake the local claimer")
	}
	got, ok := q.Get(id)
	if !ok || got.ID != id {
		t.Fatalf("Get(%q) = %#v ok=%v", id, got, ok)
	}
}

func TestSQLQueuePublishEnqueuedWakesClaimer(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	q := NewSQLQueue(sqlDB, 1)
	q.PublishEnqueued(t.Context(), "job-1")
	select {
	case <-q.RunnableWake():
	default:
		t.Fatal("published enqueue did not wake the claimer")
	}
}

func TestSQLQueueCompleteRequiresCurrentClaim(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	testdbseed.InsertSession(t, sqlDB, "parent-1", testdbseed.DefaultProjectID)
	q := NewSQLQueue(sqlDB, 1)
	id, err := q.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
		ParentSessionID: "parent-1",
		ProjectID:       testdbseed.DefaultProjectID,
		AgentType:       "implementer",
		Prompt:          "do work",
		Brief:           "Do work",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue worker", err)

	won, err := q.Complete(t.Context(), &api.WorkerTask{
		ID: id, ClaimToken: "stale",
	}, api.WorkerResult{Status: "complete"})
	testutil.FailErr(t, "reject stale claim", err)
	if won {
		t.Fatal("stale claim completed worker")
	}
	pending, ok := q.Get(id)
	if !ok || pending.Status != api.WorkerStatusPending {
		t.Fatalf("worker after stale completion = %+v want pending", pending)
	}

	claimed, err := q.ClaimNext(t.Context(), ClaimRequest{
		ClaimedBy:       "test",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "claim worker", err)
	won, err = q.Complete(t.Context(), claimed, api.WorkerResult{Status: "complete"})
	testutil.FailErr(t, "complete claimed worker", err)
	if !won {
		t.Fatal("current claim did not complete worker")
	}
	completed, ok := q.Get(id)
	if !ok || completed.Status != api.WorkerStatusComplete {
		t.Fatalf("worker after claimed completion = %+v want complete", completed)
	}
}

func TestSQLQueuePersistsOutcomeDeliveryAcknowledgement(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "parent-1", testdbseed.DefaultProjectID)
	queue := NewSQLQueue(database, 1)
	id, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
		ParentSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID,
		AgentType: "implementer", Prompt: "do work", Brief: "Do work",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue worker", err)
	claimed, err := queue.ClaimNext(t.Context(), ClaimRequest{
		ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "claim worker", err)
	completed, err := queue.Complete(t.Context(), claimed, api.WorkerResult{Status: "complete"})
	testutil.FailErr(t, "complete worker", err)
	if !completed {
		t.Fatal("current claim did not complete worker")
	}

	reopened := NewSQLQueue(database, 1)
	pending, err := reopened.ListPendingOutcomes(t.Context())
	testutil.FailErr(t, "list pending outcomes", err)
	if len(pending) != 1 || pending[0].ID != id {
		t.Fatalf("pending outcomes = %+v", pending)
	}
	testutil.FailErr(t, "acknowledge outcome", reopened.MarkOutcomeDelivered(t.Context(), id))
	pending, err = NewSQLQueue(database, 1).ListPendingOutcomes(t.Context())
	testutil.FailErr(t, "list acknowledged outcomes", err)
	if len(pending) != 0 {
		t.Fatalf("pending outcomes after acknowledgement = %+v", pending)
	}
}

func TestSQLQueueDecisionSuspendsWithoutTerminalResult(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "parent-1", testdbseed.DefaultProjectID)
	queue := NewSQLQueue(database, 1)
	id, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
		ParentSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID,
		AgentType: "implementer", Prompt: "ask", Brief: "Ask",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue worker", err)
	claimed, err := queue.ClaimNext(t.Context(), ClaimRequest{
		ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "claim worker", err)

	testdbseed.InsertSession(t, database, "child-1", testdbseed.DefaultProjectID)
	_, err = database.ExecContext(t.Context(), `UPDATE worker_jobs SET child_session_id = ? WHERE id = ?`, "child-1", id)
	testutil.FailErr(t, "bind child", err)
	testutil.FailErr(t, "record decision", session.NewSQLDecisionStore(database).Put(t.Context(), api.WorkerDecisionRequest{ChildSessionID: "child-1", WorkerID: id, Question: "Choose", Options: []string{"A", "B"}}))
	won, err := queue.Complete(t.Context(), claimed, api.WorkerResult{Status: "needs_decision", Summary: "choose"})
	testutil.FailErr(t, "suspend worker", err)
	if !won {
		t.Fatal("decision suspension lost its claim")
	}
	task, ok := queue.Get(id)
	if !ok || task.Status != api.WorkerStatusHeld {
		t.Fatalf("decision task = %+v want held", task)
	}
	var results int
	testutil.FailErr(t, "count terminal results", database.QueryRowContext(
		t.Context(), `SELECT COUNT(*) FROM worker_results WHERE worker_job_id = ?`, id,
	).Scan(&results))
	if results != 0 {
		t.Fatalf("terminal results = %d want 0", results)
	}
	var attemptStatus string
	testutil.FailErr(t, "read attempt status", database.QueryRowContext(
		t.Context(), `SELECT status FROM worker_attempts WHERE worker_job_id = ?`, id,
	).Scan(&attemptStatus))
	if attemptStatus != "suspended" {
		t.Fatalf("attempt status = %q want suspended", attemptStatus)
	}
	pending, err := queue.ListPendingOutcomes(t.Context())
	testutil.FailErr(t, "list pending checkpoint", err)
	if len(pending) != 1 || pending[0].ID != id {
		t.Fatalf("pending checkpoints = %+v", pending)
	}
}
