package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"weak"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

func writeBudgetTree(t *testing.T, root string, wide, deep int) {
	t.Helper()
	for i := range wide {
		p := filepath.Join(root, "wide", fmt.Sprintf("w%03d", i))
		testutil.FailErr(t, "mkdir wide", os.MkdirAll(filepath.Dir(p), 0o755))
		testutil.FailErr(t, "write wide", os.WriteFile(p, []byte("x"), 0o644))
	}
	for i := range deep {
		p := filepath.Join(root, "deep", fmt.Sprintf("d%03d", i), "f")
		testutil.FailErr(t, "mkdir deep", os.MkdirAll(filepath.Dir(p), 0o755))
		testutil.FailErr(t, "write deep", os.WriteFile(p, []byte("x"), 0o644))
	}
	testutil.FailErr(t, "mkdir src", os.MkdirAll(filepath.Join(root, "src"), 0o755))
	testutil.FailErr(t, "write src", os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o644))
}

func budgetPolicy(b sandbox.SurveyBudgets) walkPolicy {
	return walkPolicy{budgets: b, base: "."}
}

func TestBuildSnapshotMarksBudgetBoundariesAndKeepsTheRestOfTheTree(t *testing.T) {
	root := t.TempDir()
	writeBudgetTree(t, root, 12, 3)
	snapshot, err := buildSnapshot(context.Background(), []Root{{ID: "r", Path: root}}, budgetPolicy(sandbox.SurveyBudgets{
		DirectoryEntries: 8, SubtreeEntries: 1000, WalkEntries: 100000,
	}))
	testutil.FailErr(t, "build", err)
	wide, ok := snapshot.Entry("r", "wide")
	if !ok || !wide.Boundary {
		t.Fatalf("wide/ over the directory cap must be a boundary entry: %+v ok=%v", wide, ok)
	}
	if listing, _ := snapshot.Listing("r", "wide"); len(listing) != 0 {
		t.Fatalf("a boundary directory holds no cataloged children, got %d", len(listing))
	}
	if _, ok := snapshot.Entry("r", "src/main.go"); !ok {
		t.Fatal("siblings of a boundary are cataloged in full")
	}
	if _, ok := snapshot.Entry("r", "deep/d001/f"); !ok {
		t.Fatal("a directory under the cap is cataloged in full")
	}
	if !snapshot.underBoundary("r", "wide/w001") || snapshot.underBoundary("r", "src/main.go") {
		t.Fatal("underBoundary answers from the boundary set")
	}
	if snapshot.RootBoundary {
		t.Fatal("the walk budget was not spent")
	}
}

func TestChangesBelowABoundaryDoNotMarkTheGenerationStale(t *testing.T) {
	root := t.TempDir()
	writeBudgetTree(t, root, 12, 1)
	catalog := New()
	catalog.SetScopes(fixedScopes{budgets: sandbox.SurveyBudgets{DirectoryEntries: 8, SubtreeEntries: 1000, WalkEntries: 100000}})
	roots := []Root{{ID: "r", Path: root}}
	snapshot, err := catalog.Snapshot(context.Background(), "p", roots)
	testutil.FailErr(t, "snapshot", err)
	if entry, ok := snapshot.Entry("r", "wide"); !ok || !entry.Boundary {
		t.Fatalf("wide/ boundary missing: %+v", entry)
	}
	before := catalog.Current(context.Background(), "p", roots)
	catalog.InvalidateRoot(root, filepath.Join(root, "wide", "w003"))
	after := catalog.Current(context.Background(), "p", roots)
	if after.Revision != before.Revision || after.Refreshing {
		t.Fatalf("a write under a boundary scheduled a reconciliation: before=%d after=%d refreshing=%v", before.Revision, after.Revision, after.Refreshing)
	}
	catalog.InvalidateRoot(root, filepath.Join(root, "src", "main.go"))
	if after := catalog.Current(context.Background(), "p", roots); after.Revision == before.Revision && !after.Refreshing {
		t.Fatal("a write inside the observed tree still reconciles")
	}
}

// fixedScopes hands every root the same catalog budgets.
type fixedScopes struct{ budgets sandbox.SurveyBudgets }

func (f fixedScopes) Catalog(_ context.Context, root string) *sourcescope.Scope {
	return sourcescope.New(root, sourcescope.Options{Plane: sourcescope.Plane{Budgets: f.budgets}})
}

func ownedCatalogScope(catalog *Catalog, entries int) (func(context.Context) error, weak.Pointer[fixedScopes]) {
	provider := &fixedScopes{budgets: sandbox.SurveyBudgets{DirectoryEntries: entries}}
	return catalog.SetScopes(provider), weak.Make(provider)
}

func TestCatalogScopeReleasePreservesReplacementAndDropsOwner(t *testing.T) {
	catalog := New()
	root := t.TempDir()
	oldRelease, oldOwner := ownedCatalogScope(catalog, 3)
	t.Cleanup(func() { testutil.FailErr(t, "release old catalog scopes", oldRelease(t.Context())) })
	currentRelease, currentOwner := ownedCatalogScope(catalog, 9)
	t.Cleanup(func() { testutil.FailErr(t, "release current catalog scopes", currentRelease(t.Context())) })
	testutil.FailErr(t, "release replaced catalog owner", oldRelease(t.Context()))
	testutil.FailErr(t, "repeat replaced owner release", oldRelease(t.Context()))
	runtime.GC()
	if oldOwner.Value() != nil {
		t.Fatal("released handle retained the old traversal provider")
	}
	if policy := catalog.Trees.policyFor(t.Context(), root); policy.budgets.DirectoryEntries != 9 {
		t.Fatalf("old owner cleanup changed current traversal budgets: %+v", policy.budgets)
	}
	testutil.FailErr(t, "release current catalog owner", currentRelease(t.Context()))
	runtime.GC()
	if currentOwner.Value() != nil {
		t.Fatal("closed catalog scope retained its policy owner")
	}
	if policy := catalog.Trees.policyFor(t.Context(), root); policy.budgets != defaultCatalogPolicy(root).budgets {
		t.Fatal("closed catalog owner did not return traversal to bundled budgets")
	}
}

type catalogScopeFunc func(context.Context, string) *sourcescope.Scope

func (f catalogScopeFunc) Catalog(ctx context.Context, root string) *sourcescope.Scope {
	return f(ctx, root)
}

func TestCatalogScopeDrainJoinsCopiedProviderBeforeRelease(t *testing.T) {
	catalog := New()
	root := t.TempDir()
	entered, canceled, resume, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(resume) })
	release := catalog.SetScopes(catalogScopeFunc(func(ctx context.Context, root string) *sourcescope.Scope {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-resume
		return sourcescope.New(root, sourcescope.Options{})
	}))
	t.Cleanup(func() { unblock(); testutil.FailErr(t, "drain scope provider", release(t.Context())) })
	catalog.Trees.scopesMu.RLock()
	copied := catalog.Trees.scopes
	catalog.Trees.scopesMu.RUnlock()
	go func() { catalog.Trees.policyFor(t.Context(), root); close(finished) }()
	<-entered
	deadline, cancel := context.WithCancel(t.Context())
	cancel()
	if err := release(deadline); !errors.Is(err, context.Canceled) {
		t.Fatalf("unfinished provider drain = %v, want cancellation evidence", err)
	}
	<-canceled
	select {
	case <-finished:
		t.Fatal("provider release completed before copied work returned")
	default:
	}
	if copied.Catalog(t.Context(), root) != nil {
		t.Fatal("a copied registration admitted work after its owner closed")
	}
	unblock()
	<-finished
	testutil.FailErr(t, "finish scope provider drain", release(t.Context()))
	if policy := catalog.Trees.policyFor(t.Context(), root); policy.budgets != defaultCatalogPolicy(root).budgets {
		t.Fatal("released process catalog retained an owner-specific traversal budget")
	}
}
