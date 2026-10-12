package structure

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func loadCheckout(t *testing.T) (*Graph, declarations) {
	t.Helper()
	root := testutil.CheckoutRoot(t)
	tree := NewFileSystem(root)
	graph, err := buildGraph(tree)
	testutil.FailErr(t, "build import graph", err)
	loaded, err := loadDeclarations(tree.Read)
	testutil.FailErr(t, "load structure declarations", err)
	return graph, loaded
}

// TestDeclaredDirectoriesMatchCheckout fails when a declaration names a
// directory with no packages, or when a large undeclared top-level directory
// appears. Ten or more member packages is the line where an accreting
// boundary must state its character instead of growing silently.
func TestDeclaredDirectoriesMatchCheckout(t *testing.T) {
	graph, loaded := loadCheckout(t)
	names := make([]string, 0, len(loaded))
	for name := range loaded {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if _, err := analyze(graph, name, loaded[name]); err != nil {
			t.Errorf("declaration %s: %v", name, err)
		}
	}
	top := map[string]bool{}
	for pkg := range graph.Packages {
		if !strings.HasPrefix(pkg, "internal/") {
			continue
		}
		name := strings.TrimPrefix(pkg, "internal/")
		if i := strings.Index(name, "/"); i >= 0 {
			name = name[:i]
		}
		top[name] = true
	}
	for name := range top {
		if _, declared := loaded["internal/"+name]; declared {
			continue
		}
		if n := len(graph.members("internal/" + name)); n >= 10 {
			t.Errorf("internal/%s holds %d member packages and is undeclared; declare it in %s",
				name, n, declarationPath)
		}
	}
}