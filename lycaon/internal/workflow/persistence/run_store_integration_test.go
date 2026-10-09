//go:build integration

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
	"github.com/lycaon/lycaon/pkg/api"
)

func TestListBySessionOrderAndFilter(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "wf.db")

	store := workflowpersistence.New(sqlDB)
	ctx := context.Background()
	sessionID := "sess-history"
	testdbseed.InsertSession(t, sqlDB, sessionID, testdbseed.DefaultProjectID)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i, st := range []api.WorkflowRunStatus{
		api.WorkflowRunStatusComplete,
		api.WorkflowRunStatusRunning,
		api.WorkflowRunStatusCanceled,
	} {
		run := &api.WorkflowRun{
			SessionID:       sessionID,
			WorkflowID:      "plan",
			WorkflowVersion: "1.0.0",
			Status:          st,
			CurrentPhase:    "stub",
			CreatedAt:       base.Add(time.Duration(i) * time.Hour),
		}
		if st != api.WorkflowRunStatusRunning {
			completed := run.CreatedAt.Add(time.Minute)
			run.CompletedAt = &completed
		}
		testutil.FailErr(t, "CreateState", store.State.CreateState(ctx, run, "", nil))
	}

	all, err := store.Runs.ListBySession(ctx, sessionID, 10, nil)
	testutil.FailErr(t, "store.Runs.ListBySession failed", err)
	if len(all) != 3 {
		t.Fatalf("len = %d", len(all))
	}
	if all[0].Status != api.WorkflowRunStatusCanceled {
		t.Fatalf("newest status = %q", all[0].Status)
	}

	limited, err := store.Runs.ListBySession(ctx, sessionID, 1, nil)
	testutil.FailErr(t, "store.Runs.ListBySession failed", err)
	if len(limited) != 1 {
		t.Fatalf("limit len = %d", len(limited))
	}

	filtered, err := store.Runs.ListBySession(ctx, sessionID, 10, []string{string(api.WorkflowRunStatusRunning)})
	testutil.FailErr(t, "store.Runs.ListBySession failed", err)
	if len(filtered) != 1 || filtered[0].Status != api.WorkflowRunStatusRunning {
		t.Fatalf("filtered = %+v", filtered)
	}
}

func TestListPageBySessionKeepsFirstPageWatermark(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "wf-page.db")
	store := workflowpersistence.New(sqlDB)
	ctx := context.Background()
	sessionID := "sess-page"
	testdbseed.InsertSession(t, sqlDB, sessionID, testdbseed.DefaultProjectID)
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	create := func(id string, at time.Time) {
		t.Helper()
		run := &api.WorkflowRun{ID: id, SessionID: sessionID, WorkflowID: "plan", WorkflowVersion: "1", Status: api.WorkflowRunStatusComplete, CurrentPhase: "done", CreatedAt: at, CompletedAt: &at}
		testutil.FailErr(t, "CreateState", store.State.CreateState(ctx, run, "", nil))
	}
	create("00000000-0000-4000-8000-000000000001", base)
	create("00000000-0000-4000-8000-000000000003", base)
	create("00000000-0000-4000-8000-000000000004", base)

	first, err := store.Runs.ListPageBySession(ctx, sessionID, 2, nil, "")
	testutil.FailErr(t, "first page", err)
	if len(first.Runs) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	create("00000000-0000-4000-8000-000000000002", base)
	second, err := store.Runs.ListPageBySession(ctx, sessionID, 2, nil, first.NextCursor)
	testutil.FailErr(t, "second page", err)
	if len(second.Runs) != 1 || second.Runs[0].ID != "00000000-0000-4000-8000-000000000001" {
		t.Fatalf("second page crossed watermark: %+v", second.Runs)
	}
	if _, err := store.Runs.ListPageBySession(ctx, sessionID, 2, []string{"running"}, first.NextCursor); !errors.Is(err, workflowpersistence.ErrInvalidRunPageCursor) {
		t.Fatalf("cursor reused with another filter: %v", err)
	}
}
