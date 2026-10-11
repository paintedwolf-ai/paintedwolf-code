package sourcecatalog

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

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
		testutil.WaitFor(t, 5*time.Second, func() bool {
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

func TestLiveWatcherInvalidatesModelLiteralCandidatesWithUnchangedMetadata(t *testing.T) {
	root := t.TempDir()
	writeLiteralFixture(t, root, "source.txt", "AlphaNeedle")
	info, err := os.Stat(filepath.Join(root, "source.txt"))
	testutil.FailErr(t, "stat initial file", err)
	catalog := liveTestCatalog(t, root)
	query := LiteralQuery{RootID: "root", Base: ".", Require: litprefilter.AnyOf("OmegaNeedle"), Open: literalTestOpener(root)}
	snapshot, err := catalog.Snapshot(t.Context(), "project", []Root{{ID: "root", Path: root}})
	testutil.FailErr(t, "initial catalog", err)
	candidates, err := catalog.Literals.LiteralCandidates(t.Context(), snapshot, query)
	testutil.FailErr(t, "warm literal cache", err)
	if len(candidates) != 0 {
		t.Fatal("unwritten literal matched the initial file")
	}
	writeLiteralFixture(t, root, "source.txt", "OmegaNeedle")
	testutil.FailErr(t, "preserve external timestamp", os.Chtimes(filepath.Join(root, "source.txt"), info.ModTime(), info.ModTime()))
	testutil.WaitFor(t, 5*time.Second, func() bool {
		current, err := catalog.Snapshot(t.Context(), "project", []Root{{ID: "root", Path: root}})
		testutil.FailErr(t, "refresh catalog", err)
		candidates, err := catalog.Literals.LiteralCandidates(t.Context(), current, query)
		testutil.FailErr(t, "query changed literal", err)
		return slices.Equal(literalCandidatePaths(candidates), []string{"source.txt"})
	})
}
