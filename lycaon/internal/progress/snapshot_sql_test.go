package progress_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/progress/progresstest"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSQLProgressRevisionSurvivesReopenAndRejectsStaleABA(t *testing.T) {
	database := testdbfixture.Open(t, "progress-snapshot")
	testdbseed.InsertSession(t, database, "session", testdbseed.DefaultProjectID)
	store := progress.NewSQLStore(database)
	testutil.FailErr(t, "seed checklist", store.Set("session", "- [ ] Deliver"))
	before, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read checklist", err)
	for _, text := range []string{"- [ ] Other scope", before.Content} {
		update, prepareErr := progress.PrepareAuthorUpdate(text)
		testutil.FailErr(t, "prepare checklist", prepareErr)
		observed, readErr := store.Snapshot(t.Context(), "session")
		testutil.FailErr(t, "observe replacement revision", readErr)
		_, applyErr := update.ExpectRevision(observed.Revision).ExpectItemIDs(observed.ItemIDs...).Apply(t.Context(), store, "session", nil)
		testutil.FailErr(t, "replace checklist", applyErr)
	}
	reopened := progress.NewSQLStore(database)
	after, err := reopened.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read reopened checklist", err)
	if before.Content != after.Content || before.Revision == after.Revision {
		t.Fatal("revision lost replacement identity")
	}
	update, err := progress.PrepareAuthorUpdate("- [x] Deliver")
	testutil.FailErr(t, "prepare stale checklist", err)
	_, err = update.ExpectRevision(before.Revision).ExpectItemIDs(before.ItemIDs...).Apply(t.Context(), reopened, "session", nil)
	var conflict *progress.AuthorUpdateRejection
	if !errors.As(err, &conflict) || conflict.Code != "PROGRESS_REVISION_CHANGED" {
		t.Fatalf("stale update result: %v", err)
	}
	current, err := reopened.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read unchanged checklist", err)
	if !current.Equal(after) {
		t.Fatal("stale update changed progress")
	}
}

func TestSQLProgressKeepsAtomicSnapshotOnRejectedOrCanceledUpdates(t *testing.T) {
	store := progresstest.New(t, "session")
	empty, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read empty progress", err)
	if empty.Content != "" || empty.Revision == "" {
		t.Fatalf("empty snapshot: %+v", empty)
	}
	testutil.FailErr(t, "seed complete progress", store.Set("session", "- [ ] Preserve"))
	before, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read progress", err)
	refusal := errors.New("replacement refused")
	_, err = store.Update(t.Context(), "session", func(s progress.Snapshot) (progress.Document, error) {
		return progress.Document{Content: "- [x] Preserve", ItemIDs: s.ItemIDs}, refusal
	})
	if !errors.Is(err, refusal) {
		t.Fatalf("replacement rejection: %v", err)
	}
	after, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read rejected replacement", err)
	if !before.Equal(after) {
		t.Fatal("rejected replacement changed snapshot")
	}
	during, cancelDuring := context.WithCancel(t.Context())
	_, err = store.Update(during, "session", func(s progress.Snapshot) (progress.Document, error) {
		cancelDuring()
		return progress.Document{Content: "- [x] Preserve", ItemIDs: s.ItemIDs}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation during publication: %v", err)
	}
	after, err = store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read canceled publication", err)
	if !before.Equal(after) {
		t.Fatal("canceled publication changed snapshot")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = store.Snapshot(ctx, "session"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled snapshot: %v", err)
	}
	_, err = store.Update(ctx, "session", func(progress.Snapshot) (progress.Document, error) {
		t.Error("canceled update entered publication")
		return progress.Document{}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled update: %v", err)
	}
}

func TestSQLProgressReplacesChecklistWithPlainTextOrEmptyDocument(t *testing.T) {
	store := progresstest.New(t, "session")
	store.BindRun("session", "run-1")
	t.Run("set prose", func(t *testing.T) {
		testutil.FailErr(t, "seed checklist", store.Set("session", "- [ ] Deliver"))
		testutil.FailErr(t, "replace checklist with prose", store.Set("session", "Deliver the changes."))
		snapshot, err := store.Snapshot(t.Context(), "session")
		testutil.FailErr(t, "read replacement", err)
		if snapshot.Content != "Deliver the changes." || len(snapshot.ItemIDs) != 0 || snapshot.RunID != "run-1" {
			t.Fatalf("plain progress snapshot = %+v", snapshot)
		}
	})
	t.Run("clear through update", func(t *testing.T) {
		testutil.FailErr(t, "seed checklist", store.Set("session", "- [ ] Deliver"))
		before, err := store.Snapshot(t.Context(), "session")
		testutil.FailErr(t, "read checklist", err)
		_, err = store.Update(t.Context(), "session", func(progress.Snapshot) (progress.Document, error) {
			return progress.Document{}, nil
		})
		testutil.FailErr(t, "clear checklist", err)
		after, err := store.Snapshot(t.Context(), "session")
		testutil.FailErr(t, "read cleared progress", err)
		if after.Content != "" || len(after.ItemIDs) != 0 || after.RunID != before.RunID || after.Revision == before.Revision {
			t.Fatalf("cleared progress snapshot = %+v", after)
		}
	})
}
