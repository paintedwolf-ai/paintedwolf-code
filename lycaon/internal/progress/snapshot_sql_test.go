package progress_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/progress/progresstest"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSQLProgressVersionAndSequenceSurviveReopenAndDetectStaleBasis(t *testing.T) {
	database := testdbfixture.Open(t, "progress-snapshot")
	testdbseed.InsertWorkflowRun(t, database, "run", "session", testdbseed.DefaultProjectID)
	store := progress.NewSQLStore(database, sessionstore.NewSQL(database))

	first, err := store.Apply(t.Context(), "session", progress.Mutation{
		Replace: []progress.RowSpec{{Label: "Deliver", State: progress.ProgressStatePending}},
	}, progress.ApplyOptions{})
	testutil.FailErr(t, "seed rows", err)
	basis := progress.CaptureBasis(progress.Mutation{
		Replace: []progress.RowSpec{{Label: "Stale replacement", State: progress.ProgressStatePending}},
	}, first.Snapshot)

	_, err = store.Apply(t.Context(), "session", progress.Mutation{
		Replace: []progress.RowSpec{{Label: "Other scope", State: progress.ProgressStatePending}},
	}, progress.ApplyOptions{})
	testutil.FailErr(t, "replace rows", err)

	reopened := progress.NewSQLStore(database, sessionstore.NewSQL(database))
	after, err := reopened.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read reopened rows", err)
	if after.DocumentVersion() != 2 || after.NextItemSeq() != 3 || after.Rows[0].ID != "p2" {
		t.Fatalf("reopened snapshot = %+v, ver=%d seq=%d", after, after.DocumentVersion(), after.NextItemSeq())
	}

	_, err = reopened.Apply(t.Context(), "session", progress.Mutation{
		Replace: []progress.RowSpec{{Label: "Stale replacement", State: progress.ProgressStatePending}},
	}, progress.ApplyOptions{Basis: basis})
	var stale *progress.MutationRejection
	if !errors.As(err, &stale) || stale.Code != "PROGRESS_STALE_BASIS" {
		t.Fatalf("stale replace result: %v", err)
	}
	current, err := reopened.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read unchanged rows", err)
	if !current.Equal(after) {
		t.Fatal("stale replace mutated progress")
	}
}

func TestSQLProgressKeepsAtomicSnapshotOnRejectedOrCanceledUpdates(t *testing.T) {
	store := progresstest.New(t, "session")
	empty, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read empty progress", err)
	if !empty.Missing() || empty.DocumentVersion() != 0 || empty.NextItemSeq() != 1 {
		t.Fatalf("empty snapshot: %+v", empty)
	}
	seeded, err := store.Apply(t.Context(), "session", progress.Mutation{
		Replace: []progress.RowSpec{{Label: "Preserve", State: progress.ProgressStatePending}},
	}, progress.ApplyOptions{})
	testutil.FailErr(t, "seed progress", err)
	before := seeded.Snapshot

	done := progress.ProgressStateDone
	_, err = store.Apply(t.Context(), "session", progress.Mutation{
		Update: []progress.UpdateRowSpec{{ID: "p99", State: &done}},
	}, progress.ApplyOptions{})
	var missing *progress.MutationRejection
	if !errors.As(err, &missing) || missing.Code != "PROGRESS_ITEM_NOT_FOUND" {
		t.Fatalf("unknown ID rejection: %v", err)
	}
	after, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read after rejection", err)
	if !before.Equal(after) {
		t.Fatal("rejected update changed snapshot")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = store.Snapshot(ctx, "session"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled snapshot: %v", err)
	}
	if _, err = store.Apply(ctx, "session", progress.Mutation{
		Update: []progress.UpdateRowSpec{{ID: "p1", State: &done}},
	}, progress.ApplyOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled apply: %v", err)
	}
}

func TestSQLProgressClearPreservesNextItemSeq(t *testing.T) {
	store := progresstest.New(t, "session")
	_, err := store.Apply(t.Context(), "session", progress.Mutation{
		Replace: []progress.RowSpec{
			{Label: "First", State: progress.ProgressStatePending},
			{Label: "Second", State: progress.ProgressStatePending},
		},
	}, progress.ApplyOptions{})
	testutil.FailErr(t, "seed rows", err)
	testutil.FailErr(t, "clear rows", store.Clear(t.Context(), "session"))
	cleared, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read cleared snapshot", err)
	if !cleared.Missing() || cleared.DocumentVersion() != 2 || cleared.NextItemSeq() != 3 {
		t.Fatalf("cleared snapshot = %+v ver=%d seq=%d", cleared, cleared.DocumentVersion(), cleared.NextItemSeq())
	}
	next, err := store.Apply(t.Context(), "session", progress.Mutation{
		Replace: []progress.RowSpec{{Label: "Post-clear", State: progress.ProgressStatePending}},
	}, progress.ApplyOptions{})
	testutil.FailErr(t, "seed post-clear row", err)
	if next.Snapshot.Rows[0].ID != "p3" {
		t.Fatalf("post-clear ID = %q, want p3", next.Snapshot.Rows[0].ID)
	}
}
