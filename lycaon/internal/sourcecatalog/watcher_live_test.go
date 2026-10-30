package sourcecatalog

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

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
