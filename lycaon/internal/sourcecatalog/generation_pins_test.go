package sourcecatalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func rootExtent(t *testing.T, navigation *Navigation) int64 {
	t.Helper()
	children, err := navigation.Children(t.Context(), ".")
	testutil.FailErr(t, "open root ranks", err)
	extent, err := children.Extent(t.Context())
	testutil.FailErr(t, "read root extent", err)
	return extent
}

// Pins preserve generation coordinates until their final reference is released.
func TestRetainedGenerationSurvivesPublicationUntilReleased(t *testing.T) {
	catalog, root := indexFixture(t)
	for i := range 3 {
		writeIndexFile(t, root.Path, fmt.Sprintf("dir-%d/a.txt", i), "source")
	}
	_, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe root", err)
	head, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open head", err)
	generation := head.Generation
	if extent := rootExtent(t, head); extent != 3 {
		t.Fatalf("initial extent=%d", extent)
	}
	entry, err := head.Entry(t.Context(), "dir-1")
	testutil.FailErr(t, "resolve entry from pages", err)
	if !entry.IsDir || entry.Name != "dir-1" || entry.Parent != "." || entry.Depth != 1 {
		t.Fatalf("entry=%+v", entry)
	}
	pin, err := head.Retain()
	testutil.FailErr(t, "close head", head.Close())
	testutil.FailErr(t, "pin generation", err)
	for i := range 3 {
		_, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, fmt.Sprintf("dir-%d", i), DirectoryRead{Priority: backgroundwork.PriorityInteractive})
		testutil.FailErr(t, "list child directory", err)
	}
	testutil.FailErr(t, "remove a directory", os.RemoveAll(filepath.Join(root.Path, "dir-2")))
	catalog.InvalidateRoot(root.Path, "dir-2")
	_, err = catalog.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "publish removal", err)
	pinned, err := pin.OpenNavigation(t.Context())
	testutil.FailErr(t, "open pinned generation", err)
	if extent := rootExtent(t, pinned); extent != 3 {
		t.Fatalf("pinned extent moved: %d", extent)
	}
	removed, err := pinned.Entry(t.Context(), "dir-2")
	testutil.FailErr(t, "resolve removed entry in pinned generation", err)
	if !removed.IsDir {
		t.Fatalf("removed entry=%+v", removed)
	}
	testutil.FailErr(t, "close pinned", pinned.Close())
	current, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open current head", err)
	if extent := rootExtent(t, current); extent != 4 {
		t.Fatalf("head extent=%d", extent)
	}
	if _, err := current.Entry(t.Context(), "dir-2"); !errors.Is(err, pagedview.ErrMissing) {
		t.Fatalf("head still resolves the removed directory: %v", err)
	}
	testutil.FailErr(t, "close current", current.Close())
	pin.Release()
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get store", err)
	store.mu.Lock()
	checkpointDone := store.checkpoint.Drain()
	store.mu.Unlock()
	if checkpointDone != nil {
		<-checkpointDone
	}
	if _, err := pin.OpenNavigation(t.Context()); !errors.Is(err, pagedview.ErrExpired) {
		t.Fatalf("released generation still opens: %v", err)
	}
	store.mu.Lock()
	_, retained := store.pins.held[generation]
	store.mu.Unlock()
	if retained {
		t.Fatal("released generation remains retained")
	}
}

func TestGenerationCannotBeRetainedWithoutAnExistingReference(t *testing.T) {
	catalog, root := indexFixture(t)
	nav, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open snapshot", err)
	pin, err := nav.Retain()
	testutil.FailErr(t, "retain snapshot", err)
	testutil.FailErr(t, "close snapshot", nav.Close())
	next, err := pin.OpenNavigation(t.Context())
	testutil.FailErr(t, "open retained coordinates", err)
	pin.Release()
	copy, err := next.Retain()
	testutil.FailErr(t, "retain from active read", err)
	testutil.FailErr(t, "close read", next.Close())
	writeIndexFile(t, root.Path, "successor.txt", "new generation")
	_, err = catalog.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{})
	testutil.FailErr(t, "publish successor generation", err)
	copy.Release()
	if _, err := copy.OpenNavigation(t.Context()); !errors.Is(err, pagedview.ErrExpired) {
		t.Fatalf("unretained generation accepted: %v", err)
	}
}

func TestOnlyReferencedGenerationAndHeadSurviveIntermediatePublications(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "first.txt", "source")
	_, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{})
	testutil.FailErr(t, "observe initial directory", err)
	initial, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open initial structure", err)
	pin, err := initial.Retain()
	testutil.FailErr(t, "retain initial structure", err)
	defer pin.Release()
	testutil.FailErr(t, "close initial snapshot", initial.Close())
	var intermediate int64
	for i := range 12 {
		name := fmt.Sprintf("file-%d.txt", i)
		writeIndexFile(t, root.Path, name, "source")
		catalog.InvalidateRoot(root.Path, name)
		_, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{})
		testutil.FailErr(t, "publish changed membership", err)
		if i == 5 {
			current, openErr := catalog.Directories.OpenNavigation(t.Context(), "p", root)
			testutil.FailErr(t, "open intermediate generation", openErr)
			intermediate = current.Generation
			testutil.FailErr(t, "close intermediate generation", current.Close())
		}
	}
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "resolve store", err)
	store.mu.Lock()
	checkpointDone := store.checkpoint.Drain()
	store.mu.Unlock()
	if checkpointDone != nil {
		select {
		case <-checkpointDone:
		case <-t.Context().Done():
			t.Fatal("checkpoint did not release its generation pin")
		}
	}
	store.mu.Lock()
	retained := len(store.pins.held)
	_, oldHeld := store.pins.held[pin.Generation]
	store.mu.Unlock()
	if retained != 1 || !oldHeld {
		t.Fatalf("retained generations=%d, old held=%t", retained, oldHeld)
	}
	if _, err := openNavigation(t.Context(), store, intermediate); !errors.Is(err, pagedview.ErrRevision) {
		t.Fatalf("unreferenced intermediate generation opens: %v", err)
	}
	old, err := pin.OpenNavigation(t.Context())
	testutil.FailErr(t, "open retained generation", err)
	defer func() { _ = old.Close() }()
	if count := rootExtent(t, old); count != 1 {
		t.Fatalf("retained extent=%d", count)
	}
}

func TestReleaseTreeRootPreservesPinnedGeneration(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "first.txt", "source")
	_, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{})
	testutil.FailErr(t, "observe initial directory", err)
	navigation, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open navigation", err)
	pin, err := navigation.Retain()
	testutil.FailErr(t, "retain generation", err)
	testutil.FailErr(t, "close navigation", navigation.Close())
	defer pin.Release()

	testutil.FailErr(t, "release detached root", catalog.Trees.ReleaseTreeRoot(t.Context(), root.Path))
	retained, err := pin.OpenNavigation(t.Context())
	testutil.FailErr(t, "open navigation from retired pin", err)
	testutil.FailErr(t, "close retained navigation", retained.Close())
	directory, found, err := pin.value.directories.Get(t.Context(), ".")
	testutil.FailErr(t, "read pinned directory", err)
	if !found {
		t.Fatal("pinned root directory missing after release")
	}
	if _, err := pin.value.Read(t.Context(), directory.page); err != nil {
		t.Fatalf("pinned page unreadable after release: %v", err)
	}
	pin.Release()
	if _, err := pin.OpenNavigation(t.Context()); !errors.Is(err, pagedview.ErrExpired) {
		t.Fatalf("released pin reopened navigation: %v", err)
	}
}
