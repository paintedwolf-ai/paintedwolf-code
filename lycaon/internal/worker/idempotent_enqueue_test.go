package worker

import (
	"context"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestEnqueueReplaysSourceToolCallAndRejectsDifferentArguments(t *testing.T) {
	ctx := context.Background()
	database := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	testdbseed.InsertSessionWithRoot(t, database, "parent-1", testdbseed.DefaultProjectID, root)
	queue := NewSQLQueue(database, 2)
	task := api.WorkerTask{
		ParentSessionID: "parent-1", SourceToolCallID: "call-1", SourceArgsDigest: "digest-a",
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: root, AgentType: "implementer",
		Status: api.WorkerStatusPending, ExecutionTarget: api.ExecutionTargetLocal,
		Prompt: "Inspect the project", Brief: "Inspect the project",
	}
	firstID, err := queue.Enqueue(ctx, task)
	testutil.FailErr(t, "enqueue first task", err)
	replayedID, err := queue.Enqueue(ctx, task)
	testutil.FailErr(t, "replay task enqueue", err)
	if replayedID != firstID {
		t.Fatalf("replayed id = %s want %s", replayedID, firstID)
	}
	task.SourceArgsDigest = "digest-b"
	if _, err := queue.Enqueue(ctx, task); err == nil {
		t.Fatal("expected source tool-call conflict")
	}
	jobs, err := queue.ListBySession(ctx, testdbseed.DefaultProjectID, "parent-1")
	testutil.FailErr(t, "list jobs", err)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d want 1", len(jobs))
	}
}

func TestMemoryEnqueueReplaysSourceToolCallAndRejectsDifferentArguments(t *testing.T) {
	ctx := context.Background()
	queue := NewInMemoryQueue(2)
	task := api.WorkerTask{
		ParentSessionID: "parent-1", SourceToolCallID: "call-1", SourceArgsDigest: "digest-a",
		ProjectID: "project-1", WorkspacePath: t.TempDir(), AgentType: "implementer",
		Status: api.WorkerStatusPending, ExecutionTarget: api.ExecutionTargetLocal,
		Prompt: "Inspect the project", Brief: "Inspect the project",
	}
	firstID, err := queue.Enqueue(ctx, task)
	testutil.FailErr(t, "enqueue first task", err)
	replayedID, err := queue.Enqueue(ctx, task)
	testutil.FailErr(t, "replay task enqueue", err)
	if replayedID != firstID {
		t.Fatalf("replayed id = %s want %s", replayedID, firstID)
	}
	task.SourceArgsDigest = "digest-b"
	if _, err := queue.Enqueue(ctx, task); err == nil {
		t.Fatal("expected source tool-call conflict")
	}
	jobs, err := queue.List(ctx, "project-1")
	testutil.FailErr(t, "list jobs", err)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d want 1", len(jobs))
	}
}

func TestReviewJobInsertionFencesDuplicateReservations(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	testdbseed.InsertSessionWithRoot(t, database, "parent-1", testdbseed.DefaultProjectID, root)
	testdbseed.InsertWorkflowRun(t, database, "run", "parent-1", testdbseed.DefaultProjectID)
	queries := db.New(database)
	testutil.FailErr(t, "record subject", queries.InsertWorkflowReviewSubject(t.Context(), db.InsertWorkflowReviewSubjectParams{ID: "subject", RunID: "run", Phase: "challenge", Revision: "revision", SubjectJson: "{}"}))
	for _, id := range []string{"first", "second"} {
		testutil.FailErr(t, "reserve assignment", queries.InsertWorkflowReviewAssignment(t.Context(), db.InsertWorkflowReviewAssignmentParams{ID: id, RunID: "run", SubjectID: "subject", Phase: "challenge", WorkID: "review/skeptic", Agent: "skeptic", BindingJson: "{}"}))
	}
	store := NewSQLStore(database)
	task := api.WorkerTask{ID: "first", ParentSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: root, AgentType: "skeptic", Status: api.WorkerStatusPending, ExecutionTarget: api.ExecutionTargetLocal, Prompt: "Review the subject", Brief: "Review the subject", WorkflowRunID: "run", WorkflowPhase: "challenge", WorkflowWorkID: "review/skeptic"}
	testutil.FailErr(t, "insert first reservation", store.InsertTask(t.Context(), task))
	task.ID = "second"
	rejected := tools.AsToolReject(store.InsertTask(t.Context(), task))
	if rejected == nil || rejected.Data["action"] != "wait_for_work" {
		t.Fatalf("duplicate reservation was admitted: %+v", rejected)
	}
	jobs, _ := rejected.Data["job_ids"].([]string)
	if len(jobs) != 1 || jobs[0] != "first" {
		t.Fatalf("missing active job identity: %+v", rejected.Data)
	}
	_, err := database.ExecContext(t.Context(), "UPDATE worker_jobs SET status='canceled' WHERE id='first'")
	testutil.FailErr(t, "cancel first review", err)
	testutil.FailErr(t, "insert replacement", store.InsertTask(t.Context(), task))
	revision, err := queries.GetWorkflowReviewInputRevision(t.Context(), "run")
	testutil.FailErr(t, "read review input revision", err)
	if revision != 3 {
		t.Fatalf("worker inserts and status changes produced epoch %d, want 3", revision)
	}
}
