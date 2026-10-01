package survey

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

// inventoryPaths walks the whole scope and records each entry with its depth below it.
func inventoryPaths(t *testing.T, inventory sourceInventory) map[string]int {
	t.Helper()
	out := map[string]int{}
	err := inventory.walk(t.Context(), func(entry sourcecatalog.Entry) sourcecatalog.WalkStep {
		out[entry.Path] = inventory.depth(entry)
		return sourcecatalog.WalkContinue
	})
	testutil.FailErr(t, "walk inventory", err)
	return out
}

func writeInventoryTree(t *testing.T, dir string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write "+rel, os.WriteFile(abs, []byte("package x\n"), 0o644))
	}
}

func TestSourceInventoryForScopeColdRootPublishesCatalog(t *testing.T) {
	dir := t.TempDir()
	writeInventoryTree(t, dir, "a.go")

	catalog := sourcecatalog.New()
	root := projectroot.RootRef{ID: "root", Path: dir}
	inventory, err := sourceInventoryForScope(context.Background(), catalog, "proj", root, dir)
	testutil.FailErr(t, "root entries", err)
	if got := inventoryPaths(t, inventory); inventory.base != "." || len(got) != 1 || got["a.go"] != 1 {
		t.Fatalf("base=%q entries=%v", inventory.base, got)
	}
}

// A cold parent serves a subdirectory from its own scoped generation, rebased
// so paths and depths read as if the whole root had been walked.
func TestSourceInventoryForScopeSubdirUsesScopedSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeInventoryTree(t, dir, "pkg/a.go", "pkg/inner/b.go", "other/c.go")

	catalog := sourcecatalog.New()
	root := projectroot.RootRef{ID: "root", Path: dir}
	inventory, err := sourceInventoryForScope(context.Background(), catalog, "proj", root, filepath.Join(dir, "pkg"))
	testutil.FailErr(t, "scoped entries", err)
	if !inventory.rebased || inventory.base != "pkg" {
		t.Fatalf("scoped inventory rebased=%v base=%q", inventory.rebased, inventory.base)
	}
	want := map[string]int{"pkg/a.go": 1, "pkg/inner": 1, "pkg/inner/b.go": 2}
	if got := inventoryPaths(t, inventory); !maps.Equal(got, want) {
		t.Fatalf("scoped entries = %v, want %v", got, want)
	}
}

// A ready parent generation serves the subdirectory without a second walk.
func TestSourceInventoryForScopeSubdirReusesReadyParent(t *testing.T) {
	dir := t.TempDir()
	writeInventoryTree(t, dir, "pkg/a.go", "pkg/inner/b.go", "other/c.go")

	catalog := sourcecatalog.New()
	root := projectroot.RootRef{ID: "root", Path: dir}
	_, err := sourceInventoryForScope(context.Background(), catalog, "proj", root, dir)
	testutil.FailErr(t, "warm parent", err)
	inventory, err := sourceInventoryForScope(context.Background(), catalog, "proj", root, filepath.Join(dir, "pkg"))
	testutil.FailErr(t, "subdirectory entries", err)
	if inventory.rebased || inventory.catalogRoot != "root" {
		t.Fatalf("subdirectory did not reuse the parent generation: %+v", inventory)
	}
	want := map[string]int{"pkg/a.go": 1, "pkg/inner": 1, "pkg/inner/b.go": 2}
	if got := inventoryPaths(t, inventory); !maps.Equal(got, want) {
		t.Fatalf("subdirectory entries = %v, want %v", got, want)
	}
}

func TestSourceInventoryForScopeRefreshesAfterAgentMutationInvalidation(t *testing.T) {
	dir := t.TempDir()
	writeInventoryTree(t, dir, "a.go")
	catalog := sourcecatalog.New()
	root := projectroot.RootRef{ID: "root", Path: dir}
	_, err := sourceInventoryForScope(context.Background(), catalog, "proj", root, dir)
	testutil.FailErr(t, "warm agent source inventory", err)

	writeInventoryTree(t, dir, "late.go")
	catalog.InvalidateRoot(dir)
	inventory, err := sourceInventoryForScope(context.Background(), catalog, "proj", root, dir)
	testutil.FailErr(t, "refresh agent source inventory", err)
	got := inventoryPaths(t, inventory)
	if _, ok := got["late.go"]; !ok {
		t.Fatalf("agent inventory = %v, want late.go", slices.Sorted(maps.Keys(got)))
	}
}
