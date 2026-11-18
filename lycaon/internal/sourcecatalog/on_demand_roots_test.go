package sourcecatalog

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A root whose discovery policy is on demand lists only what a reader opens:
// warming and awaiting navigation read nothing below it.
func TestOnDemandRootListsOnlyOpenedFolders(t *testing.T) {
	c := New()
	t.Cleanup(func() { testutil.FailErr(t, "drain index discovery", c.Drain(context.Background())) })
	dir := t.TempDir()
	writeIndexFile(t, dir, "notes/a.txt", "a")
	writeIndexFile(t, dir, "deep/er/est/b.txt", "b")
	writeIndexFile(t, dir, "top.txt", "top")
	root := Root{ID: "location", Path: dir, OnDemand: true}

	testutil.FailErr(t, "warm navigation", c.Directories.WarmNavigation(t.Context(), "p", root))
	testutil.FailErr(t, "await navigation", c.Directories.AwaitNavigation(t.Context(), "p", root))
	nav, err := c.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open navigation", err)
	listed := nav.pages.directories.Len()
	nav.Close()
	if listed > 1 {
		t.Fatalf("an on-demand root listed %d directories before any was opened", listed)
	}
	top, err := c.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "open the root", err)
	if !top.Complete || top.Entries != 3 {
		t.Fatalf("opening the root listed %+v, want its three entries", top)
	}
	nested, err := c.Directories.ObserveDirectory(t.Context(), "p", root, "deep", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "open a folder", err)
	if !nested.Complete || nested.Entries != 1 {
		t.Fatalf("opening a folder listed %+v, want only its own entry", nested)
	}
}

// A root that lists one entry shows that entry alone, never its siblings.
func TestOnlyEntryRootHidesSiblings(t *testing.T) {
	c := New()
	t.Cleanup(func() { testutil.FailErr(t, "drain index discovery", c.Drain(context.Background())) })
	dir := t.TempDir()
	writeIndexFile(t, dir, "config", "Host example")
	writeIndexFile(t, dir, "id_ed25519", "secret")
	root := Root{ID: "location", Path: dir, OnDemand: true, Only: "config"}

	top, err := c.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "open the root", err)
	if !top.Complete || top.Entries != 1 {
		t.Fatalf("a one-entry root listed %+v", top)
	}
	if rootKey("p", root) == rootKey("p", Root{ID: "location", Path: dir}) {
		t.Fatal("a one-entry root shares its listing with the whole folder")
	}
}
