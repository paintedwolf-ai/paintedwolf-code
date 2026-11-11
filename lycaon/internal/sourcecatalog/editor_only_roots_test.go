package sourcecatalog

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/rootfacts"
)

// An editor-only root costs what the person opens. Discovery records
// the root without listing it, and a folder is read when it is opened.
func TestEditorOnlyRootIsObservedOnDemand(t *testing.T) {
	facts := rootfacts.Install(t)
	c := New()
	t.Cleanup(func() { testutil.FailErr(t, "drain index discovery", c.Drain(context.Background())) })
	for _, root := range facts.EditorOnly() {
		t.Run(root.Name, func(t *testing.T) {
			writeIndexFile(t, root.Path, "notes/a.txt", "a")
			writeIndexFile(t, root.Path, "deep/er/b.txt", "b")
			catalogRoot := Root{ID: root.Name, Path: root.Path}
			reader := waitIndex(t, c, catalogRoot)
			coverage, err := reader.Coverage(t.Context())
			testutil.FailErr(t, "coverage", err)
			if !coverage.Exhaustive() {
				t.Fatalf("an on-demand root reads as a discovery failure: %+v", coverage)
			}
			if paths := indexedPaths(t, reader, FileScope{Audience: HumanAudience, IncludeHidden: true}); len(paths) != 0 {
				t.Fatalf("eager discovery listed %v below an editor-only root", paths)
			}
			testutil.FailErr(t, "warm navigation", c.Directories.WarmNavigation(t.Context(), "p", catalogRoot))
			testutil.FailErr(t, "await navigation", c.Directories.AwaitNavigation(t.Context(), "p", catalogRoot))
			nav, err := c.Directories.OpenNavigation(t.Context(), "p", catalogRoot)
			testutil.FailErr(t, "open navigation", err)
			if listed := nav.pages.directories.Len(); listed > 1 {
				t.Fatalf("navigation warm-up listed %d directories below an editor-only root", listed)
			}
			nav.Close()
			observation, err := c.Directories.ObserveDirectory(t.Context(), "p", catalogRoot, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
			testutil.FailErr(t, "open the root", err)
			if !observation.Complete || observation.Entries < 2 {
				t.Fatalf("opening the root listed %+v", observation)
			}
			nested, err := c.Directories.ObserveDirectory(t.Context(), "p", catalogRoot, "notes", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
			testutil.FailErr(t, "open a folder", err)
			if !nested.Complete || nested.Entries != 1 {
				t.Fatalf("opening a folder listed %+v", nested)
			}
		})
	}
}

// The engine's own state is a boundary wherever a walk reaches it, and a
// recursive request below it does not cross it.
func TestControlPlaneIsABoundaryWhereverReached(t *testing.T) {
	facts := rootfacts.Install(t)
	writeIndexFile(t, facts.State, "store.db", "state")
	writeIndexFile(t, facts.ContainsState, "readme.txt", "ordinary")
	policy := defaultCatalogPolicy(facts.ContainsState)
	stateRel, err := filepath.Rel(facts.ContainsState, facts.State)
	testutil.FailErr(t, "state rel", err)
	if reason := policy.boundaryDir(filepath.ToSlash(stateRel)); reason == "" {
		t.Fatal("the state tree is not a boundary")
	}
	dependencies := policy.under(".")
	dependencies.includeDependencies = true
	if reason := dependencies.boundaryDir(filepath.ToSlash(stateRel)); reason == "" {
		t.Fatal("a dependency walk crosses the state tree")
	}
	if reason := dependencies.boundaryPath(filepath.ToSlash(filepath.Join(stateRel, "store.db")), false); reason == "" {
		t.Fatal("a file in the state tree is outside the boundary under a dependency walk")
	}
}
