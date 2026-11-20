package sourcecatalog

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWatcherRestartInvalidatesChangesFromTheUnwatchedInterval(t *testing.T) {
	root := Root{ID: "root", Path: t.TempDir()}
	writeCatalogFile(t, root.Path, "first.txt")
	start := func() func(context.Context) error {
		release := repochange.AcquireWatcherLifetime()
		t.Cleanup(func() {
			if err := release(context.Background()); err != nil {
				t.Errorf("release watcher lifetime: %v", err)
			}
		})
		repochange.EnsureRoot(t.Context(), root.Path)
		testutil.WaitFor(t, "root watch is complete", func() bool { return repochange.Coverage(root.Path).Complete })
		return release
	}
	release := start()
	catalog := Process()
	first := settledSnapshot(t, catalog, root)
	if _, settled := catalog.CurrentSettled(t.Context(), "p", []Root{root}); !settled {
		t.Fatal("initial catalog was not settled")
	}
	testutil.FailErr(t, "retire watcher lifetime", release(t.Context()))
	writeCatalogFile(t, root.Path, "second.txt")
	start()
	if _, settled := catalog.CurrentSettled(t.Context(), "p", []Root{root}); settled {
		t.Fatal("reopened stream certified the catalog from before the unwatched interval")
	}
	next := settledSnapshot(t, catalog, root)
	if next.Revision == first.Revision {
		t.Fatal("watcher restart did not publish the changed tree")
	}
}
