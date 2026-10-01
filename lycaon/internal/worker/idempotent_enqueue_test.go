package worker

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
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
