package progress_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
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
		_, applyErr := update.ExpectRevision(observed.Revision).Apply(t.Context(), store, "session", nil)
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
	_, err = update.ExpectRevision(before.Revision).Apply(t.Context(), reopened, "session", nil)
	var conflict *progress.AuthorUpdateRejection
	if !errors.As(err, &conflict) || conflict.Code != "PROGRESS_REVISION_CHANGED" {
		t.Fatalf("stale update result: %v", err)
	}
	current, err := reopened.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read unchanged checklist", err)
	if current != after {
		t.Fatal("stale update changed progress")
	}
}

func TestProgressStoresKeepAtomicSnapshotOnRejectedOrCanceledUpdates(t *testing.T) {
	for _, name := range []string{"memory", "sql"} {
		t.Run(name, func(t *testing.T) {
			var store progress.Store = progress.NewMemoryStore()
			if name == "sql" {
				database := testdbfixture.Open(t, "progress-owner-atomic")
				testdbseed.InsertSession(t, database, "session", testdbseed.DefaultProjectID)
				store = progress.NewSQLStore(database)
			}
			empty, err := store.Snapshot(t.Context(), "session")
			testutil.FailErr(t, "read empty progress", err)
			if empty.Content != "" || empty.Revision == "" {
				t.Fatalf("empty snapshot: %+v", empty)
			}
			testutil.FailErr(t, "seed complete progress", store.Set("session", "- [ ] Preserve"))
			before, err := store.Snapshot(t.Context(), "session")
			testutil.FailErr(t, "read progress", err)
			refusal := errors.New("replacement refused")
			_, err = store.Update(t.Context(), "session", func(progress.Snapshot) (string, error) { return "- [x] Preserve", refusal })
			if !errors.Is(err, refusal) {
				t.Fatalf("replacement rejection: %v", err)
			}
			after, err := store.Snapshot(t.Context(), "session")
			testutil.FailErr(t, "read rejected replacement", err)
			if before != after {
				t.Fatal("rejected replacement changed snapshot")
			}
			during, cancelDuring := context.WithCancel(t.Context())
			_, err = store.Update(during, "session", func(progress.Snapshot) (string, error) {
				cancelDuring()
				return "- [x] Preserve", nil
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation during publication: %v", err)
			}
			after, err = store.Snapshot(t.Context(), "session")
			testutil.FailErr(t, "read canceled publication", err)
			if before != after {
				t.Fatal("canceled publication changed snapshot")
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err = store.Snapshot(ctx, "session"); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled snapshot: %v", err)
			}
			_, err = store.Update(ctx, "session", func(progress.Snapshot) (string, error) {
				t.Error("canceled update entered publication")
				return "", nil
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled update: %v", err)
			}
		})
	}
}
