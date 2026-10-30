package progress_test

import (
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
		_, applyErr := update.Apply(t.Context(), store, "session")
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
	_, err = update.ExpectRevision(before.Revision).Apply(t.Context(), reopened, "session")
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
