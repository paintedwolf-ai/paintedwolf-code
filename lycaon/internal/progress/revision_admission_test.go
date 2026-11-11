package progress_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProgressCreationCannotBlindlyReplaceAnExistingDocument(t *testing.T) {
	for _, name := range []string{"memory", "sql"} {
		t.Run(name, func(t *testing.T) {
			var store progress.Store = progress.NewMemoryStore()
			if name == "sql" {
				database := testdbfixture.Open(t, "progress-first-creation")
				testdbseed.InsertSession(t, database, "session", testdbseed.DefaultProjectID)
				store = progress.NewSQLStore(database)
			}
			var workers sync.WaitGroup
			results := make(chan error, 2)
			for _, content := range []string{"- [ ] First", "- [ ] Second"} {
				update, err := progress.PrepareAuthorUpdate(content)
				testutil.FailErr(t, "prepare first creation", err)
				workers.Add(1)
				go func() {
					defer workers.Done()
					_, err := update.Apply(t.Context(), store, "session", nil)
					results <- err
				}()
			}
			workers.Wait()
			close(results)
			accepted, refused := 0, 0
			for err := range results {
				if err == nil {
					accepted++
					continue
				}
				var rejection *progress.AuthorUpdateRejection
				if !errors.As(err, &rejection) || rejection.Code != "PROGRESS_REVISION_REQUIRED" {
					t.Fatalf("blind replacement outcome: %v", err)
				}
				snapshot, ok := rejection.Data["snapshot"].(progress.Snapshot)
				if !ok || snapshot.Content == "" || snapshot.Revision == "" {
					t.Fatalf("missing recovery snapshot: %+v", rejection.Data)
				}
				refused++
			}
			if accepted != 1 || refused != 1 {
				t.Fatalf("first creators accepted=%d refused=%d", accepted, refused)
			}
			snapshot, err := store.Snapshot(t.Context(), "session")
			testutil.FailErr(t, "observe winning creation", err)
			update, err := progress.PrepareAuthorUpdate(snapshot.Content)
			testutil.FailErr(t, "prepare identical replacement", err)
			if _, err := update.Apply(t.Context(), store, "session", nil); err == nil {
				t.Fatal("identical bytes bypassed revision requirement")
			}
			result, err := update.ExpectRevision(snapshot.Revision).ExpectItemIDs(snapshot.ItemIDs...).Apply(t.Context(), store, "session", nil)
			testutil.FailErr(t, "submit observed no-op", err)
			if result.Updated || result.Snapshot.Revision != snapshot.Revision {
				t.Fatal("observed no-op changed revision")
			}
		})
	}
}
