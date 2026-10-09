package persistence_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSQLStoreWorkflowRunCRUD(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := workflowpersistence.New(sqlDB)
	testdbseed.InsertSession(t, sqlDB, "sess-1", testdbseed.DefaultProjectID)
	run := &api.WorkflowRun{
		SessionID:       "sess-1",
		WorkflowID:      "plan",
		WorkflowVersion: "1.0.0",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "research",
	}
	if err := store.State.CreateState(context.Background(), run, "", nil); err != nil {
		testutil.FailErr(t, "create session in store", err)
	}
	if run.ID == "" {
		t.Fatal("expected id assigned")
	}
	got, err := store.Runs.Get(context.Background(), run.ID)
	testutil.FailErr(t, "store.Runs.Get failed", err)
	if got.CurrentPhase != "research" {
		t.Fatalf("phase = %q", got.CurrentPhase)
	}
	if err := store.State.UpdateVars(context.Background(), run, "/tmp/p", map[string]any{"k": "v"}); err != nil {
		testutil.FailErr(t, "store.State.UpdateVars failed", err)
	}
}
func TestWorkflowRunRevisionRejectsStaleLifecycleWrite(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, sqlDB, "session-revision", testdbseed.DefaultProjectID)
	store := workflowpersistence.New(sqlDB)
	ctx := context.Background()
	run := &api.WorkflowRun{
		SessionID: "session-revision", WorkflowID: "test", WorkflowVersion: "1.0.0",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "work",
	}
	testutil.FailErr(t, "CreateState", store.State.CreateState(ctx, run, "", nil))
	first, err := store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get first", err)
	stale, err := store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get stale", err)

	first.Status = api.WorkflowRunStatusCanceled
	first.UpdatedAt = time.Now().UTC()
	first.CompletedAt = new(first.UpdatedAt)
	testutil.FailErr(t, "cancel", store.State.Update(ctx, first))
	stale.Status = api.WorkflowRunStatusPaused
	stale.UpdatedAt = time.Now().UTC()
	if err := store.State.Update(ctx, stale); !errors.Is(err, runstate.ErrRevisionConflict) {
		t.Fatalf("stale update err = %v want runstate.ErrRevisionConflict", err)
	}
	got, err := store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get final", err)
	if got.Status != api.WorkflowRunStatusCanceled {
		t.Fatalf("stale write resurrected status %q", got.Status)
	}
}
