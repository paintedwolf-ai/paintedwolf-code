package worker

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerprogress"
	"github.com/lycaon/lycaon/pkg/api"
	"sync"
	"testing"
)

func TestSQLStoreClaimNextAtomic(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := NewSQLStore(sqlDB)
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	task := api.WorkerTask{
		ProjectID:       testdbseed.DefaultProjectID,
		AgentType:       "implementer",
		Prompt:          "fixture",
		Brief:           "fixture",
		ExecutionTarget: api.ExecutionTargetLocal,
		Status:          api.WorkerStatusPending,
	}
	if err := store.InsertTask(context.Background(), task); err != nil {
		testutil.FailErr(t, "store.InsertTask failed", err)
	}
	pending, err := store.List(context.Background(), testdbseed.DefaultProjectID, api.WorkerStatusPending)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %#v err=%v", pending, err)
	}
	wantID := pending[0].ID

	var wg sync.WaitGroup
	claims := make(chan string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := store.ClaimNext(context.Background(), ClaimRequest{
				ClaimedBy:       "claimer",
				ExecutionTarget: api.ExecutionTargetLocal,
			}, 0, nil)
			if errors.Is(err, ErrNoPendingJobs) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			claims <- got.ID
		}()
	}
	wg.Wait()
	close(claims)

	var ids []string
	for id := range claims {
		ids = append(ids, id)
	}
	if len(ids) != 1 {
		t.Fatalf("expected exactly one claim, got %d ids=%v", len(ids), ids)
	}
	if ids[0] != wantID {
		t.Fatalf("claimed id = %q want %q", ids[0], wantID)
	}
}

func TestSQLStoreProgressIsScopedToWorkerJob(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := NewSQLStore(sqlDB)
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	base := api.WorkerTask{
		ProjectID:       testdbseed.DefaultProjectID,
		AgentType:       "repo-researcher",
		Prompt:          "fixture",
		Brief:           "fixture",
		ExecutionTarget: api.ExecutionTargetLocal,
		Status:          api.WorkerStatusPending,
	}
	first := base
	first.ID = "11111111-1111-4111-8111-111111111111"
	second := base
	second.ID = "22222222-2222-4222-8222-222222222222"
	testutil.FailErr(t, "insert first worker", store.InsertTask(t.Context(), first))
	testutil.FailErr(t, "insert second worker", store.InsertTask(t.Context(), second))
	secondBefore, ok := store.getTask(t.Context(), second.ID)
	if !ok {
		t.Fatal("second worker not found before progress update")
	}

	testutil.FailErr(t, "set first progress", store.SetProgress(t.Context(), first.ID, workerprogress.Snapshot{
		ToolLoopsUsed: 12,
		MaxToolLoops:  24,
		ToolCallsUsed: 45,
	}))

	gotFirst, ok := store.getTask(t.Context(), first.ID)
	if !ok {
		t.Fatal("first worker not found")
	}
	gotSecond, ok := store.getTask(t.Context(), second.ID)
	if !ok {
		t.Fatal("second worker not found")
	}
	if gotFirst.ToolLoopsUsed != 12 || gotFirst.ToolCallsUsed != 45 {
		t.Fatalf("first progress = %+v", gotFirst)
	}
	// A round checkpoint never writes the ceiling, so it cannot undo a grant.
	if gotFirst.MaxToolLoops != secondBefore.MaxToolLoops {
		t.Fatalf("first ceiling = %d want unchanged %d", gotFirst.MaxToolLoops, secondBefore.MaxToolLoops)
	}
	if gotSecond.ToolLoopsUsed != secondBefore.ToolLoopsUsed ||
		gotSecond.MaxToolLoops != secondBefore.MaxToolLoops ||
		gotSecond.ToolCallsUsed != secondBefore.ToolCallsUsed {
		t.Fatalf("second progress changed = %+v", gotSecond)
	}
}

func TestSQLStoreListByWorkspacePathMatchesSandboxBucket(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := NewSQLStore(sqlDB)
	dir := t.TempDir()
	testdbseed.InsertProject(t, sqlDB, "project-a")
	testdbseed.InsertProject(t, sqlDB, "project-b")
	ctx := context.Background()

	task := api.WorkerTask{
		ProjectID:       "project-a",
		WorkspacePath:   dir,
		AgentType:       "implementer",
		Prompt:          "fixture",
		Brief:           "fixture",
		ExecutionTarget: api.ExecutionTargetLocal,
		Status:          api.WorkerStatusRunning,
		Scope:           &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"src"}},
	}
	if err := store.InsertTask(ctx, task); err != nil {
		testutil.FailErr(t, "InsertTask", err)
	}

	task.ID = "unrelated"
	task.WorkspacePath = t.TempDir()
	testutil.FailErr(t, "insert unrelated workspace", store.InsertTask(ctx, task))
	_, err := sqlDB.ExecContext(ctx, `UPDATE worker_jobs SET result_json = '[1]' WHERE id = 'unrelated'`)
	testutil.FailErr(t, "isolate workspace payload", err)
	filtered, err := store.ListByWorkspacePath(ctx, dir, api.WorkerStatusComplete)
	testutil.FailErr(t, "filter workspace status", err)
	if len(filtered) != 0 {
		t.Fatalf("completed jobs = %+v", filtered)
	}

	byPath, err := store.ListByWorkspacePath(ctx, dir+"/")
	testutil.FailErr(t, "ListByWorkspacePath", err)
	if len(byPath) != 1 {
		t.Fatalf("byPath len = %d want 1", len(byPath))
	}
	byOtherProject, err := store.List(ctx, "project-b")
	testutil.FailErr(t, "List project-b", err)
	if len(byOtherProject) != 0 {
		t.Fatalf("project-b list = %d want 0", len(byOtherProject))
	}
}
