package sourcecatalog

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDependencyIndexIsFreshPrivateAndRemovedOnClose(t *testing.T) {
	catalog, root := indexFixture(t)
	catalog.SetScopes(testScopes{plane: sourcescope.Plane{BoundaryDirectories: []string{"node_modules"}, NestedCheckouts: true}})
	writeIndexFile(t, root.Path, "node_modules/other/unselected.go", "package other")
	writeIndexFile(t, root.Path, "main.go", "package main")
	writeIndexFile(t, root.Path, "node_modules/pkg/dependency.go", "package dependency")
	eager := waitIndex(t, catalog, root)
	if paths := indexedPaths(t, eager, FileScope{}); slices.Contains(paths, "node_modules/pkg/dependency.go") {
		t.Fatalf("eager index expanded dependencies: %v", paths)
	}
	selected, err := catalog.Trees.OpenDependencyIndex(t.Context(), "p", root, "node_modules/pkg/dependency.go")
	testutil.FailErr(t, "open selected dependency file", err)
	if paths := indexedPaths(t, selected, FileScope{}); !slices.Equal(paths, []string{"node_modules/pkg/dependency.go"}) {
		t.Fatalf("selected dependency index broadened scope: %v", paths)
	}
	testutil.FailErr(t, "close selected dependency index", selected.Close())
	if _, err := catalog.Trees.OpenDependencyIndex(t.Context(), "p", root, "../outside"); !os.IsPermission(err) {
		t.Fatalf("escaped selection admitted: %v", err)
	}
	reader, err := catalog.Trees.OpenDependencyIndex(t.Context(), "p", root)
	testutil.FailErr(t, "open private dependency index", err)
	privateDir := filepath.Dir(reader.store.file)
	if paths := indexedPaths(t, reader, FileScope{}); !slices.Contains(paths, "node_modules/pkg/dependency.go") {
		t.Fatalf("private index missed dependency: %v", paths)
	}
	testutil.FailErr(t, "close private index", reader.Close())
	if _, err := os.Stat(privateDir); !os.IsNotExist(err) {
		t.Fatalf("private index survives close: %v", err)
	}
	writeIndexFile(t, root.Path, "node_modules/pkg/later.go", "package later")
	next, err := catalog.Trees.OpenDependencyIndex(t.Context(), "p", root)
	testutil.FailErr(t, "open fresh dependency index", err)
	defer func() { testutil.FailErr(t, "close fresh dependency index", next.Close()) }()
	if paths := indexedPaths(t, next, FileScope{}); !slices.Contains(paths, "node_modules/pkg/later.go") {
		t.Fatalf("request reused stale dependency index: %v", paths)
	}
	if paths := indexedPaths(t, eager, FileScope{}); slices.Contains(paths, "node_modules/pkg/later.go") {
		t.Fatalf("dependency request modified eager index: %v", paths)
	}
}

func TestDependencySummaryIsFreshScopedAndRemovedOnClose(t *testing.T) {
	catalog, root := indexFixture(t)
	catalog.SetScopes(testScopes{plane: sourcescope.Plane{BoundaryDirectories: []string{"node_modules"}}})
	writeIndexFile(t, root.Path, "node_modules/pkg/first.go", "package dependency")
	writeIndexFile(t, root.Path, "private/hidden.go", "package private")
	scope := TreeScope{Key: "dependencies-only", Filter: func(rel string, _ bool) bool {
		return rel == "." || rel == "node_modules" || strings.HasPrefix(rel, "node_modules/")
	}}
	reader, _, err := catalog.Trees.OpenDependencySummary(t.Context(), "p", root, scope)
	testutil.FailErr(t, "open dependency summary", err)
	node, err := reader.Node(t.Context(), "node_modules/pkg/first.go")
	testutil.FailErr(t, "read dependency declaration", err)
	if node.Path != "node_modules/pkg/first.go" {
		t.Fatalf("dependency node = %+v", node)
	}
	if _, err := reader.Node(t.Context(), "private/hidden.go"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("private summary ignored read scope: %v", err)
	}
	var sequence int
	var name, database string
	testutil.FailErr(t, "read private database location", reader.tx.QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&sequence, &name, &database))
	testutil.FailErr(t, "close private summary", reader.Close())
	if _, err := os.Stat(filepath.Dir(database)); !os.IsNotExist(err) {
		t.Fatalf("private summary survives close: %v", err)
	}
	writeIndexFile(t, root.Path, "node_modules/pkg/later.go", "package later")
	next, _, err := catalog.Trees.OpenDependencySummary(t.Context(), "p", root, scope)
	testutil.FailErr(t, "open fresh dependency summary", err)
	defer func() { testutil.FailErr(t, "close fresh summary", next.Close()) }()
	if _, err := next.Node(t.Context(), "node_modules/pkg/later.go"); err != nil {
		t.Fatalf("summary reused stale dependency data: %v", err)
	}
}
