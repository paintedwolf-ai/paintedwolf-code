package sourceapi

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/project/projecttest"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/repochange/repochangetest"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSecondProjectBindingAndSubscriberResyncPreserveCoveredPhysicalObservations(t *testing.T) {
	repochangetest.Acquire(t)
	inventory := &countingInventory{}
	server := newSourceHandlerFixture(t, func(d *Deps) {
		d.WatchNeedsSeed = func(string) bool { return false }
		d.SourceInventory = inventory
	})

	sharedDir := t.TempDir()
	testutil.FailErr(t, "mkdir src", os.MkdirAll(filepath.Join(sharedDir, "src"), 0o755))
	testutil.FailErr(t, "write main.go", os.WriteFile(filepath.Join(sharedDir, "src/main.go"), []byte("package main\n"), 0o600))

	projA, err := projecttest.CreateWithRoot(t.Context(), server.Watch.ProjectRegistry, sharedDir)
	testutil.FailErr(t, "create projA", err)
	t.Cleanup(func() { _ = sourcefeed.StopProjectWatch(context.Background(), projA.ID) })

	projB, err := projecttest.CreateWithRoot(t.Context(), server.Watch.ProjectRegistry, sharedDir)
	testutil.FailErr(t, "create projB", err)
	t.Cleanup(func() { _ = sourcefeed.StopProjectWatch(context.Background(), projB.ID) })

	// Bind Project A first to establish complete physical watcher coverage on sharedDir.
	if !server.Watch.ensureWorkspaceWatch(t.Context(), projA) {
		t.Fatal("projA watch failed to bind")
	}
	repochange.SeedWatch(t.Context(), sharedDir, []repochange.WatchDirectory{
		{Path: sharedDir, EntryCount: 1},
		{Path: filepath.Join(sharedDir, "src"), EntryCount: 1},
	})
	server.Views.background.Wait(context.Background())
	testutil.WaitFor(t, "sharedDir watcher complete", func() bool {
		return repochange.Coverage(sharedDir).Complete && repochange.DirWatched(sharedDir, ".")
	})

	snapDir := t.TempDir()
	snapStore := sourcesnapshot.New(
		testdbfixture.Open(t, "snap.db"),
		sourceblob.New(filepath.Join(snapDir, "blobs")),
		filepath.Join(snapDir, "obs.db"),
		nil,
	)
	t.Cleanup(func() { _ = snapStore.Close() })

	_, err = snapStore.Ensure(t.Context(), sourcesnapshot.Request{
		Roots: []sourcesnapshot.Root{{Path: sharedDir}},
	})
	testutil.FailErr(t, "projA snapshot ensure", err)

	cat := sourcecatalog.New()
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", cat.Drain(context.Background())) })
	rootA := sourcecatalog.Root{ID: projA.Roots[0].ID, Path: sharedDir}
	rootB := sourcecatalog.Root{ID: projB.Roots[0].ID, Path: sharedDir}
	testutil.FailErr(t, "projA await navigation", cat.Directories.AwaitNavigation(t.Context(), projA.ID, rootA))

	var resyncEvents int
	var eventsMu sync.Mutex
	t.Cleanup(repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.Kind != repochange.WorktreeChanged {
			return
		}
		for _, ch := range ev.Changes {
			if ch.Kind == repochange.WorktreeChangeResync {
				eventsMu.Lock()
				resyncEvents++
				eventsMu.Unlock()
			}
		}
	}))

	epochBeforeB := repochange.CurrentEpoch(sharedDir)
	snapBeforeB := snapStore.Metrics()
	catBeforeB := cat.Directories.Metrics()
	invBeforeB := inventory.count()

	// Bind Project B on the already-covered checkout and replay resync-only subscriber notifications.
	if !server.Watch.ensureWorkspaceWatch(t.Context(), projB) {
		t.Fatal("projB watch failed to bind")
	}
	for range 3 {
		server.Watch.observeWorkspaceChanges(t.Context(), projB, sourcefeed.ExternalBatch{Resync: true})
	}
	server.Views.background.Wait(context.Background())

	// Subscriber inventory reconciled for projB without invalidating covered physical observations.
	if inventory.count() <= invBeforeB {
		t.Fatalf("subscriber inventory did not reconcile on projB bind/resync: %d -> %d", invBeforeB, inventory.count())
	}
	eventsMu.Lock()
	gotResyncs := resyncEvents
	eventsMu.Unlock()
	if gotResyncs != 0 {
		t.Fatalf("binding second project on covered checkout emitted %d physical resync events, want 0", gotResyncs)
	}
	if repochange.CurrentEpoch(sharedDir) != epochBeforeB {
		t.Fatal("covered physical epoch advanced on second project bind or subscriber resync")
	}

	_, err = snapStore.Ensure(t.Context(), sourcesnapshot.Request{
		Roots: []sourcesnapshot.Root{{Path: sharedDir}},
	})
	testutil.FailErr(t, "projB snapshot ensure", err)
	if afterSnap := snapStore.Metrics(); afterSnap.Surveys != snapBeforeB.Surveys {
		t.Fatalf("second project ensure ran %d physical surveys, want 0", afterSnap.Surveys-snapBeforeB.Surveys)
	}

	testutil.FailErr(t, "projB await navigation", cat.Directories.AwaitNavigation(t.Context(), projB.ID, rootB))
	if afterCat := cat.Directories.Metrics(); afterCat.PhysicalDirectoryReads != catBeforeB.PhysicalDirectoryReads || afterCat.BoundaryEncodes != catBeforeB.BoundaryEncodes {
		t.Fatalf("projB navigation did duplicate physical/encode work: %+v -> %+v", catBeforeB, afterCat)
	}
	if uncovered := uncoveredWorkspaceRoots(nil); uncovered != nil {
		t.Fatalf("uncoveredWorkspaceRoots(nil) = %v, want nil", uncovered)
	}
}
