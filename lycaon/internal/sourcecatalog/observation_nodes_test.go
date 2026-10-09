package sourcecatalog

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestObservationRefreshPrunesAcrossPagesAndRetainsPreviousRanks(t *testing.T) {
	catalog, root := indexFixture(t)
	for i := range 600 {
		writeIndexFile(t, root.Path, fmt.Sprintf("file-%04d.txt", i), "source")
	}
	_, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{})
	testutil.FailErr(t, "observe initial children", err)
	before, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "retain initial children", err)
	defer func() { _ = before.Close() }()
	for i := 0; i < 600; i += 2 {
		testutil.FailErr(t, "remove alternating children", os.Remove(filepath.Join(root.Path, fmt.Sprintf("file-%04d.txt", i))))
	}
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "open catalog", err)
	store.mu.Lock()
	store.invalidateObservationsLocked(nil)
	store.mu.Unlock()
	_, err = catalog.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{})
	testutil.FailErr(t, "reconcile children", err)
	after, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open current children", err)
	defer func() { _ = after.Close() }()
	if old, current := navigationExtent(t, before), navigationExtent(t, after); old != 600 || current != 300 {
		t.Fatalf("retained=%d current=%d", old, current)
	}
	reader := waitIndex(t, catalog, root)
	count, err := reader.FileCount(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true})
	testutil.FailErr(t, "count indexed survivors", err)
	if count != 300 {
		t.Fatalf("indexed=%d, want 300", count)
	}
}

func TestObservationReplacesDirectoryWithInternalLink(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "changed/old.txt", "old")
	writeIndexFile(t, root.Path, "target/new.txt", "new")
	for _, dir := range []string{".", "changed", "target"} {
		_, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, dir, DirectoryRead{})
		testutil.FailErr(t, "observe directories", err)
	}
	testutil.FailErr(t, "remove directory", os.RemoveAll(filepath.Join(root.Path, "changed")))
	testutil.FailErr(t, "replace with link", os.Symlink("target", filepath.Join(root.Path, "changed")))
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "open catalog", err)
	store.mu.Lock()
	store.invalidateObservationsLocked(nil)
	store.mu.Unlock()
	for _, dir := range []string{".", "changed"} {
		_, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, dir, DirectoryRead{})
		testutil.FailErr(t, "observe replacement", err)
	}
	entries, _, err := navigationEntries(t, catalog, root, "changed", "", 10)
	testutil.FailErr(t, "read replacement children", err)
	if len(entries) != 1 || entries[0].Name != "new.txt" {
		t.Fatalf("replacement children=%+v", entries)
	}
}

func TestObservationBatchPrunesReplacedDirectory(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "kept.txt", "old")
	writeIndexFile(t, root.Path, "changed/child.txt", "child")
	for _, dir := range []string{".", "changed"} {
		_, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, dir, DirectoryRead{Priority: backgroundwork.PriorityInteractive})
		testutil.FailErr(t, "observe initial directory", err)
	}
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get catalog", err)
	before, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open retained structure", err)
	defer func() { _ = before.Close() }()
	writeIndexFile(t, root.Path, "kept.txt", "updated metadata")
	testutil.FailErr(t, "remove original directory", os.RemoveAll(filepath.Join(root.Path, "changed")))
	writeIndexFile(t, root.Path, "changed", "replacement")
	store.mu.Lock()
	store.invalidateObservationsLocked([]string{"."})
	store.mu.Unlock()
	_, err = catalog.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "publish replacement batch", err)
	oldChildren, err := before.Children(t.Context(), "changed")
	testutil.FailErr(t, "open retained descendants", err)
	oldCount, err := oldChildren.Count(t.Context())
	testutil.FailErr(t, "count retained descendants", err)
	if oldCount != 1 {
		t.Fatalf("retained descendants=%d, want 1", oldCount)
	}
	after, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open replacement structure", err)
	defer func() { _ = after.Close() }()
	replacement, err := after.Entry(t.Context(), "changed")
	testutil.FailErr(t, "read replacement entry", err)
	if replacement.IsDir || replacement.IsSymlink || replacement.Name != "changed" {
		t.Fatalf("replacement entry=%+v", replacement)
	}
	currentChildren, err := after.Children(t.Context(), "changed")
	testutil.FailErr(t, "open replacement descendants", err)
	currentCount, err := currentChildren.Count(t.Context())
	testutil.FailErr(t, "count replacement descendants", err)
	if currentCount != 0 {
		t.Fatalf("replacement retained %d descendants", currentCount)
	}
}
