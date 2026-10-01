package sourcecatalog

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func watchRestoreRoot(t *testing.T, path string) {
	t.Helper()
	repochange.EnsureRoot(t.Context(), path)
	t.Cleanup(func() { repochange.CloseRoot(path) })
	if !repochange.DirWatched(path, ".") {
		t.Fatal("restore fixture has no live watcher")
	}
}

// awaitQuietStructure waits for the head generation to stop advancing, so a
// checkpoint and the observations compared against it describe the same pass.
func awaitQuietStructure(t *testing.T, store *indexStore) {
	t.Helper()
	settled := int64(-1)
	testutil.WaitFor(t, 30*time.Second, func() bool {
		pin, err := store.retainGeneration(headGeneration, true)
		testutil.FailErr(t, "read head generation", err)
		current := pin.Generation
		pin.Release()
		quiet := current == settled
		settled = current
		if !quiet {
			time.Sleep(300 * time.Millisecond)
		}
		return quiet
	})
}

func fixtureStamp(t *testing.T, dir string) DirectoryStamp {
	t.Helper()
	info, err := os.Lstat(dir)
	testutil.FailErr(t, "stat fixture directory", err)
	return directoryStampOf(info)
}

// writeRestoreCheckpoint seals one root listing with the given stamp and saves
// it where the store restores from.
func writeRestoreCheckpoint(t *testing.T, store *indexStore, stamp DirectoryStamp, unresolved bool, complete bool) {
	t.Helper()
	builder, err := newStructuralBuilder(store, nil)
	testutil.FailErr(t, "create cached structure", err)
	defer builder.close()
	index := &pagedview.RangeIndex[TreeItem]{Store: builder}
	if unresolved {
		item, err := builder.item(t.Context(), indexNode{path: "unknown", name: "unknown", isDir: true})
		testutil.FailErr(t, "construct unknown cached child", err)
		testutil.FailErr(t, "add unknown cached child", index.SetBatch(t.Context(), []pagedview.RangeItem[TreeItem]{item}))
	}
	observation := DirectoryObservation{Path: ".", Sequence: 1, FirstListed: 1, Complete: complete,
		Observed: time.Now(), Epoch: repochange.CurrentEpoch(store.root.Path), Stamp: stamp}
	testutil.FailErr(t, "save cached root", builder.save(t.Context(), index, observation))
	generation, err := builder.seal(t.Context(), 1)
	testutil.FailErr(t, "seal cached generation", err)
	defer generation.close()
	var encoded bytes.Buffer
	testutil.FailErr(t, "encode cached generation", writeStructuralCheckpoint(t.Context(), &encoded, store.root, generation))
	testutil.FailErr(t, "write cached generation", os.WriteFile(store.structureFile, encoded.Bytes(), 0o600))
}

func TestStructuralRestoreRequiresCompleteResolvedAndWatchedCoverage(t *testing.T) {
	for _, test := range []struct {
		name       string
		complete   bool
		unresolved bool
		stamped    bool
		watched    bool
		want       bool
	}{
		{name: "incomplete root", stamped: true, watched: true},
		{name: "unknown descendant", complete: true, unresolved: true, stamped: true, watched: true},
		{name: "listing without a stamp", complete: true, watched: true},
		{name: "root outside the watcher", complete: true, stamped: true},
		{name: "complete watched root", complete: true, stamped: true, watched: true, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := checkpointTestStore(t)
			if test.watched {
				watchRestoreRoot(t, store.root.Path)
			}
			var stamp DirectoryStamp
			if test.stamped {
				stamp = fixtureStamp(t, store.root.Path)
			}
			writeRestoreCheckpoint(t, store, stamp, test.unresolved, test.complete)
			restored, stale := store.restoreStructure(t.Context())
			defer store.structure.close()
			if restored != test.want {
				t.Fatalf("restored = %v, want %v", restored, test.want)
			}
			if _, found, err := store.structure.directories.Get(t.Context(), "."); err != nil || found != test.want {
				t.Fatalf("installed root = %v (err %v), want %v", found, err, test.want)
			}
			if len(stale) != 0 {
				t.Fatalf("stale listings = %v, want none for an unchanged root", stale)
			}
		})
	}
}

// A checkpoint whose root moved still restores, because its coverage is real,
// and reports that listing for the first pass to replace.
func TestStructuralRestoreReportsListingsWhoseStampMoved(t *testing.T) {
	store := checkpointTestStore(t)
	watchRestoreRoot(t, store.root.Path)
	writeRestoreCheckpoint(t, store, DirectoryStamp{Modified: 1, Changed: 1}, false, true)
	restored, stale := store.restoreStructure(t.Context())
	defer store.structure.close()
	if !restored {
		t.Fatal("a checkpoint with real coverage was discarded instead of revalidated")
	}
	if len(stale) != 1 || stale[0] != "." {
		t.Fatalf("stale listings = %v, want the root whose stamp moved", stale)
	}
	store.mu.Lock()
	queued := len(store.inventory.dirty)
	mark := store.observationMarkLocked(".")
	store.mu.Unlock()
	if queued != 0 {
		t.Fatalf("restore queued %d directories on its own; seeding owns that", queued)
	}
	store.mu.Lock()
	store.invalidateListingsLocked(stale)
	store.mu.Unlock()
	observation, err := store.readObservation(t.Context(), ".")
	testutil.FailErr(t, "read restored root observation", err)
	store.mu.Lock()
	remarked := store.observationMarkLocked(".")
	dirty := len(store.inventory.dirty)
	store.mu.Unlock()
	if remarked <= mark || observation.Invalidation >= remarked || dirty != 1 {
		t.Fatalf("stale listing was not queued: mark %d -> %d, record %d, dirty %d",
			mark, remarked, observation.Invalidation, dirty)
	}
}

func TestStructuralRestoreRejectsUnstampedHeaderBeforeReadingPages(t *testing.T) {
	store := checkpointTestStore(t)
	var output bytes.Buffer
	header := structuralCheckpointHeader{Format: structuralFormat, RootPath: []byte(store.root.Path), Generation: 1, Directories: 1,
		RootObservation: DirectoryObservation{Complete: true, Observed: time.Now()}}
	testutil.FailErr(t, "encode unusable header", writeStructuralJSON(&output, 'H', header))
	// No body follows, so only a header rejection can pass.
	testutil.FailErr(t, "write unusable header", os.WriteFile(store.structureFile, output.Bytes(), 0o600))
	if _, err := loadStructuralCheckpoint(t.Context(), store, true); err == nil {
		t.Fatal("a checkpoint with no directory stamp was decoded")
	}
}

// A checkpoint written by one engine restores in the next, under a different
// project and root id, keeping every directory whose stamp still matches and
// relisting only the ones that changed while nothing was running.
func TestStructuralCheckpointRestoresAcrossEnginesByDirectoryStamp(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	rootPath := t.TempDir()
	treeDir := t.TempDir()
	for _, rel := range []string{"src/a.txt", "src/deep/b.txt", "other/c.txt"} {
		writeIndexFile(t, rootPath, rel, "content")
	}
	watchRestoreRoot(t, rootPath)
	dirs := []string{".", "src", "src/deep", "other"}

	first := New()
	first.treeDir = treeDir
	firstRoot := Root{ID: "r1", Path: rootPath}
	testutil.FailErr(t, "start first inventory", first.WarmNavigation(t.Context(), "p1", firstRoot))
	testutil.FailErr(t, "await first inventory", first.AwaitNavigation(t.Context(), "p1", firstRoot))
	firstStore, err := first.indexStore(t.Context(), "p1", firstRoot)
	testutil.FailErr(t, "open first store", err)
	awaitQuietStructure(t, firstStore)
	before := make(map[string]DirectoryObservation, len(dirs))
	for _, dir := range dirs {
		before[dir], err = firstStore.readObservation(t.Context(), dir)
		testutil.FailErr(t, "read first observation", err)
		if !before[dir].Stamp.known() {
			t.Fatalf("listing of %q recorded no directory stamp", dir)
		}
	}
	written, err := firstStore.checkpointStructure(t.Context())
	testutil.FailErr(t, "write checkpoint", err)
	if !written {
		t.Fatal("complete structure was not checkpointed")
	}
	testutil.FailErr(t, "drain first engine", first.Drain(context.Background()))

	// One directory changes while no engine is running.
	time.Sleep(20 * time.Millisecond)
	testutil.FailErr(t, "add a file while nothing runs",
		os.WriteFile(filepath.Join(rootPath, "other", "d.txt"), []byte("content"), 0o600))

	second := New()
	second.treeDir = treeDir
	secondRoot := Root{ID: "r2", Path: rootPath}
	t.Cleanup(func() { testutil.FailErr(t, "drain second engine", second.Drain(context.Background())) })
	testutil.FailErr(t, "start second inventory", second.WarmNavigation(t.Context(), "p2", secondRoot))
	testutil.FailErr(t, "await second inventory", second.AwaitNavigation(t.Context(), "p2", secondRoot))
	secondStore, err := second.indexStore(t.Context(), "p2", secondRoot)
	testutil.FailErr(t, "open second store", err)
	for _, dir := range []string{".", "src", "src/deep"} {
		after, err := secondStore.readObservation(t.Context(), dir)
		testutil.FailErr(t, "read restored observation", err)
		if after.Sequence != before[dir].Sequence || after.Entries != before[dir].Entries {
			t.Fatalf("unchanged %q was relisted: %+v, checkpointed %+v", dir, after, before[dir])
		}
	}
	changed, err := secondStore.readObservation(t.Context(), "other")
	testutil.FailErr(t, "read relisted observation", err)
	if changed.Sequence <= before["other"].Sequence || changed.Entries != 2 || !changed.Complete {
		t.Fatalf("changed directory was not relisted: %+v, checkpointed %+v", changed, before["other"])
	}
	navigation, err := second.OpenNavigation(t.Context(), "p2", secondRoot)
	testutil.FailErr(t, "open restored navigation", err)
	defer func() { _ = navigation.Close() }()
	if _, err := navigation.Entry(t.Context(), "other/d.txt"); err != nil {
		t.Fatalf("file added while no engine ran is missing after restore: %v", err)
	}
}
