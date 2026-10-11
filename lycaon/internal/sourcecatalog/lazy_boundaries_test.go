package sourcecatalog

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLazyBoundariesKeepEagerCatalogIndependentOfNestedCopies(t *testing.T) {
	c, root := indexFixture(t)
	writeIndexFile(t, root.Path, "src/main.go", "package main")
	for copy := range 10 {
		base := fmt.Sprintf("worktrees/copy%d", copy)
		writeIndexFile(t, root.Path, base+"/.git", "gitdir: ../repository")
		for file := range 100 {
			writeIndexFile(t, root.Path, fmt.Sprintf("%s/node_modules/pkg%d/module.js", base, file), "readable dependency")
			writeIndexFile(t, root.Path, fmt.Sprintf("%s/target/pkg%d/output", base, file), "build output")
		}
	}
	writeIndexFile(t, root.Path, "node_modules/library/index.js", "readable library")
	reader := waitIndex(t, c, root)
	coverage, err := reader.Coverage(t.Context())
	testutil.FailErr(t, "measure eager coverage", err)
	if !coverage.Exhaustive() {
		t.Fatalf("policy boundaries counted as discovery failures: %+v", coverage)
	}
	paths := indexedPaths(t, reader, FileScope{Audience: HumanAudience, IncludeHidden: true})
	if !slices.Equal(paths, []string{"src/main.go"}) {
		t.Fatalf("eager files include nested copies: %v", paths)
	}
	nav, err := c.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open navigation", err)
	defer nav.Close()
	entry, err := nav.Entry(t.Context(), "worktrees/copy0")
	testutil.FailErr(t, "read nested checkout stub", err)
	if !entry.Boundary {
		t.Fatal("nested checkout stub omitted boundary marker")
	}
	if nav.pages.directories.Len() > 4 {
		t.Fatalf("nested copies grew directory inventory to %d", nav.pages.directories.Len())
	}
	observation, err := c.Directories.ObserveDirectory(t.Context(), "p", root, "node_modules/library", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "open lazy directory on demand", err)
	if !observation.Complete || observation.Entries != 1 {
		t.Fatalf("on-demand listing: %+v", observation)
	}
	testutil.FailErr(t, "change lazy listing", os.WriteFile(filepath.Join(root.Path, "node_modules/library/new.js"), []byte("new content"), 0600))
	refreshed, err := c.Directories.ObserveDirectory(t.Context(), "p", root, "node_modules/library", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "refresh unwatched lazy directory", err)
	if refreshed.Entries != 2 {
		t.Fatalf("lazy directory reused stale listing: %+v", refreshed)
	}
}

func TestRemovedNestedCheckoutMarkerStillRefreshesUnwatchedDirectory(t *testing.T) {
	c, root := indexFixture(t)
	writeIndexFile(t, root.Path, "worktree/.git", "gitdir: ../repository")
	writeIndexFile(t, root.Path, "worktree/src/first.go", "package first")
	waitIndex(t, c, root)
	observed, err := c.Directories.ObserveDirectory(t.Context(), "p", root, "worktree/src", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe nested checkout directory", err)
	if observed.Entries != 1 {
		t.Fatalf("initial nested listing: %+v", observed)
	}
	testutil.FailErr(t, "remove nested checkout marker", os.Remove(filepath.Join(root.Path, "worktree/.git")))
	writeIndexFile(t, root.Path, "worktree/src/second.go", "package second")
	refreshed, err := c.Directories.ObserveDirectory(t.Context(), "p", root, "worktree/src", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "refresh directory after marker removal", err)
	if refreshed.Entries != 2 {
		t.Fatalf("unwatched directory reused stale membership: %+v", refreshed)
	}
}
