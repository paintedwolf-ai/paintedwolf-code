package sourcecatalog

import (
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCoverageRetainsDirectoryFailuresInItsPublishedGeneration(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "good.txt", "readable")
	previous := waitIndex(t, catalog, root)
	store, walk := indexWalkFixture(t, catalog, root)
	testutil.FailErr(t, "record directory failure", walk.faultDir(t.Context(), "private", os.ErrPermission))
	walk.covering = true
	testutil.FailErr(t, "publish settled failure", walk.publish(t.Context()))
	reader, err := openIndexReader(t, store)
	testutil.FailErr(t, "open failed coverage", err)
	coverage, err := reader.Coverage(t.Context())
	testutil.FailErr(t, "read coverage", err)
	if !coverage.DiscoveryComplete || coverage.FailedDirectories != 1 || coverage.Exhaustive() || coverage.Pending() {
		t.Fatalf("coverage = %+v", coverage)
	}
	old, err := previous.Coverage(t.Context())
	testutil.FailErr(t, "read pinned coverage", err)
	if old.FailedDirectories != 0 {
		t.Fatalf("new failure leaked into prior generation: %+v", old)
	}
}

func TestRootFileCountDoesNotMeasureABudgetLimitedTree(t *testing.T) {
	catalog, root := indexFixture(t)
	catalog.SetScopes(testScopes{plane: sourcescope.Plane{Budgets: sandbox.SurveyBudgets{DirectoryEntries: 2}}})
	writeIndexFile(t, root.Path, "visible.txt", "source")
	for _, name := range []string{"a", "b", "c"} {
		writeIndexFile(t, root.Path, "wide/"+name, "source")
	}
	waitIndex(t, catalog, root)
	count, err := catalog.Trees.RootFileCount(t.Context(), "p", root, FileScope{Audience: HumanAudience, IncludeHidden: true}, 0)
	testutil.FailErr(t, "count bounded root", err)
	if count.Count != 1 || count.Measured {
		t.Fatalf("bounded count = %+v", count)
	}
}

func TestReadableRefreshFailureSurvivesAnotherAttempt(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "good.txt", "readable")
	first := waitIndex(t, catalog, root)
	release, err := catalog.Trees.broker.Acquire(t.Context(), backgroundwork.Request{Lane: root.Path, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata}})
	testutil.FailErr(t, "hold retry", err)
	defer release()
	store := first.store
	store.mu.Lock()
	store.status.Error = "failed refresh"
	store.full = true
	store.mu.Unlock()
	reader, _, err := catalog.Trees.OpenIndex(t.Context(), "p", root, 0)
	testutil.FailErr(t, "read during retry", err)
	if reader == nil {
		t.Fatal("lost readable generation")
	}
	defer func() { _ = reader.Close() }()
	coverage, err := reader.Coverage(t.Context())
	testutil.FailErr(t, "read retry coverage", err)
	if coverage.Error != "failed refresh" || coverage.Pending() || coverage.Exhaustive() {
		t.Fatalf("retry hid refresh failure: %+v", coverage)
	}
}
