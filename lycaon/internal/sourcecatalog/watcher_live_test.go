package sourcecatalog

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func liveTestCatalog(t *testing.T, root string) *Catalog {
	t.Helper()
	catalog := New()
	unbind := repochange.RegisterObserver(func(_ context.Context, event repochange.Event) {
		if event.ProjectDir == root && event.Kind == repochange.WorktreeChanged {
			catalog.InvalidateRoot(root, event.Paths...)
		}
	})
	t.Cleanup(unbind)
	repochange.EnsureRoot(t.Context(), root)
	t.Cleanup(func() { repochange.CloseRoot(root) })
	if !repochange.Coverage(root).Watching {
		t.Fatal("live source watcher did not start")
	}
	return catalog
}

func TestLiveWatcherRefreshesCatalogForExternalFileLifecycle(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, ".gitignore", "*.log\n")
	writeLiteralFixture(t, root, "ignored.log", "ignored source")
	writeLiteralFixture(t, root, ".hidden", "hidden source")
	catalog := liveTestCatalog(t, root)
	assertPaths := func(want []string) {
		t.Helper()
		var got []string
		testutil.WaitFor(t, func() bool {
			snapshot, err := catalog.Snapshot(t.Context(), "project", []Root{{ID: "root", Path: root}})
			testutil.FailErr(t, "read live catalog", err)
			got = got[:0]
			for _, entry := range snapshot.Entries {
				got = append(got, entry.Path)
			}
			slices.Sort(got)
			return slices.Equal(got, want)
		})
	}
	assertPaths([]string{".gitignore", ".hidden", "ignored.log"})
	writeLiteralFixture(t, root, "added.txt", "new source")
	assertPaths([]string{".gitignore", ".hidden", "added.txt", "ignored.log"})
	testutil.FailErr(t, "rename external file", os.Rename(filepath.Join(root, "added.txt"), filepath.Join(root, "renamed.txt")))
	assertPaths([]string{".gitignore", ".hidden", "ignored.log", "renamed.txt"})
	testutil.FailErr(t, "delete external file", os.Remove(filepath.Join(root, "renamed.txt")))
	assertPaths([]string{".gitignore", ".hidden", "ignored.log"})
}

func TestLiveCatalogKeepsChangedContentAsCandidateWithUnchangedMetadata(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, "source.txt", "AlphaNeedle")
	info, err := os.Stat(filepath.Join(root, "source.txt"))
	testutil.FailErr(t, "stat initial file", err)
	catalog := liveTestCatalog(t, root)
	snapshot, err := catalog.Snapshot(t.Context(), "project", []Root{{ID: "root", Path: root}})
	testutil.FailErr(t, "initial catalog", err)
	if len(snapshot.Entries) != 1 {
		t.Fatalf("initial entries=%v", snapshot.Entries)
	}
	entry := snapshot.Entries[0]
	version := fspath.ObserveFile(info)
	observation := catalog.Literals.BeginRead(root, entry, version)
	file, err := os.Open(filepath.Join(root, entry.Path))
	testutil.FailErr(t, "open initial stream", err)
	_, err = io.Copy(io.Discard, observation.Wrap(t.Context(), file))
	testutil.FailErr(t, "observe initial stream", err)
	testutil.FailErr(t, "close initial stream", file.Close())
	observation.Commit()
	observation.Close()
	require := litprefilter.AnyOf("OmegaNeedle")
	if pruned := catalog.Literals.CanPrune(root, require, entry); pruned != (version.Known && version.Links == 1) {
		t.Fatalf("initial pruning=%t for version=%+v", pruned, version)
	}
	writeLiteralFixture(t, root, "source.txt", "OmegaNeedle")
	testutil.FailErr(t, "preserve external timestamp", os.Chtimes(filepath.Join(root, "source.txt"), info.ModTime(), info.ModTime()))
	testutil.WaitFor(t, func() bool {
		current, err := catalog.Snapshot(t.Context(), "project", []Root{{ID: "root", Path: root}})
		testutil.FailErr(t, "refresh catalog", err)
		return len(current.Entries) == 1 && !catalog.Literals.CanPrune(root, require, current.Entries[0])
	})
}

func TestLiveWatcherExcludesControlDirAndRuntimeChurnWithoutSelfObservationLoops(t *testing.T) {
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".paintedwolf")
	testutil.FailErr(t, "mkdir packs", os.MkdirAll(filepath.Join(cfgDir, "packs"), 0o755))
	testutil.FailErr(t, "mkdir cache", os.MkdirAll(filepath.Join(cfgDir, "cache"), 0o755))
	t.Setenv(configdir.EnvConfigDir, cfgDir)

	writeLiteralFixture(t, home, "app/main.go", "package main")
	writeLiteralFixture(t, cfgDir, "settings.yaml", "theme: dark")
	writeLiteralFixture(t, cfgDir, "paintedwolf.db", "db-v1")
	writeLiteralFixture(t, cfgDir, "paintedwolf.db-wal", "wal-v1")

	// 1. Live watcher on $HOME (strict ancestor of configdir.Root())
	homeCatalog := liveTestCatalog(t, home)
	assertSnapshotPaths := func(cat *Catalog, r string, want []string) uint64 {
		t.Helper()
		var rev uint64
		testutil.WaitFor(t, func() bool {
			snap, err := cat.Snapshot(t.Context(), "project", []Root{{ID: "root", Path: r}})
			testutil.FailErr(t, "snapshot", err)
			rev = snap.Revision
			var got []string
			for _, e := range snap.Entries {
				got = append(got, e.Path)
			}
			slices.Sort(got)
			return slices.Equal(got, want)
		})
		return rev
	}

	initialHomeRev := assertSnapshotPaths(homeCatalog, home, []string{"app", "app/main.go"})
	// Mutating SQLite DB/WAL/SHM and settings inside .paintedwolf while $HOME is watched
	// must NOT emit watcher events or add .paintedwolf entries, while sibling edits emit immediately.
	writeLiteralFixture(t, cfgDir, "paintedwolf.db-wal", "wal-v2-churn")
	writeLiteralFixture(t, cfgDir, "paintedwolf.db-shm", "shm-v2-churn")
	writeLiteralFixture(t, cfgDir, "store.db-wal", "store-wal-churn")
	writeLiteralFixture(t, cfgDir, "cache/item.bin", "cache-churn")
	writeLiteralFixture(t, home, "app/sibling.go", "package main")
	afterHomeRev := assertSnapshotPaths(homeCatalog, home, []string{"app", "app/main.go", "app/sibling.go"})
	if afterHomeRev <= initialHomeRev {
		t.Fatalf("expected revision to advance on sibling edit: before=%d after=%d", initialHomeRev, afterHomeRev)
	}

	// 2. Live watcher on directly attached configdir.Root() (cfgDir)
	cfgCatalog := liveTestCatalog(t, cfgDir)
	assertSnapshotPaths(cfgCatalog, cfgDir, []string{"packs", "settings.yaml"})
	// Mutate runtime DB/WAL/SHM/cache/lock files and then a human config file (packs/rule.yaml).
	writeLiteralFixture(t, cfgDir, "paintedwolf.db", "db-v3-churn")
	writeLiteralFixture(t, cfgDir, "paintedwolf.db-wal", "wal-v3-churn")
	writeLiteralFixture(t, cfgDir, "paintedwolf.db-shm", "shm-v3-churn")
	writeLiteralFixture(t, cfgDir, "source-catalog.db-wal", "cat-wal-churn")
	writeLiteralFixture(t, cfgDir, "runtime.json", `{"pid":123}`)
	writeLiteralFixture(t, cfgDir, "daemon.lock", "locked")
	writeLiteralFixture(t, cfgDir, "cache/state.bin", "state-churn")
	writeLiteralFixture(t, cfgDir, "packs/rule.yaml", "id: r1")
	assertSnapshotPaths(cfgCatalog, cfgDir, []string{"packs", "packs/rule.yaml", "settings.yaml"})
}
