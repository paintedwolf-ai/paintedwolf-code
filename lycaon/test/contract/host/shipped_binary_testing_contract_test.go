package contract

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// testOnlyPackages may only be imported by test files.
var testOnlyPackages = []string{
	"github.com/lycaon/lycaon/internal/browserengine/browsertest",
	"github.com/lycaon/lycaon/internal/prompts/promptstest",
	"github.com/lycaon/lycaon/internal/report/reporttest",
	"github.com/lycaon/lycaon/internal/testdbseed",
	"github.com/lycaon/lycaon/internal/testutil",
}

// TestShippedBinariesDoNotLinkTesting checks first-party command dependencies.
func TestShippedBinariesDoNotLinkTesting(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	moduleRoot := filepath.Join(root, "lycaon")
	const modulePath = "github.com/lycaon/lycaon"

	fileImports := scanGoImports(t, moduleRoot)

	// Collapse file imports into the internal package graph.
	pkgImports := map[string]map[string]bool{}
	importsTesting := map[string]bool{}
	for path, imps := range fileImports {
		rel, err := filepath.Rel(moduleRoot, filepath.Dir(path))
		if err != nil {
			contractcheck.FailErr(t, "relativize package dir", err)
		}
		pkg := modulePath
		if rel != "." {
			pkg = modulePath + "/" + filepath.ToSlash(rel)
		}
		if pkgImports[pkg] == nil {
			pkgImports[pkg] = map[string]bool{}
		}
		for _, imp := range imps {
			if imp == "testing" {
				importsTesting[pkg] = true
			}
			if strings.HasPrefix(imp, modulePath+"/") {
				pkgImports[pkg][imp] = true
			}
		}
	}

	var entrypoints []string
	for pkg := range pkgImports {
		if strings.HasPrefix(pkg, modulePath+"/cmd/") {
			entrypoints = append(entrypoints, pkg)
		}
	}
	sort.Strings(entrypoints)
	if len(entrypoints) == 0 {
		t.Fatal("no cmd/* packages found; walker or module layout changed")
	}

	testOnly := map[string]bool{}
	for _, pkg := range testOnlyPackages {
		testOnly[pkg] = true
	}

	var violations []string
	for _, entry := range entrypoints {
		// BFS with parent pointers so failures print the full import chain.
		parent := map[string]string{entry: ""}
		queue := []string{entry}
		for len(queue) > 0 {
			pkg := queue[0]
			queue = queue[1:]
			if importsTesting[pkg] || testOnly[pkg] {
				violations = append(violations, fmt.Sprintf("  %s reaches %s via %s", entry, pkg, importChain(parent, pkg)))
				continue
			}
			deps := make([]string, 0, len(pkgImports[pkg]))
			for dep := range pkgImports[pkg] {
				deps = append(deps, dep)
			}
			sort.Strings(deps)
			for _, dep := range deps {
				if _, seen := parent[dep]; seen {
					continue
				}
				parent[dep] = pkg
				queue = append(queue, dep)
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("shipped binaries must not link testing or test-only helper packages;\n"+
			"move the helper into a _test.go file or a test-only package (see testOnlyPackages):\n%s",
			strings.Join(violations, "\n"))
	}
}

func importChain(parent map[string]string, pkg string) string {
	var chain []string
	for p := pkg; p != ""; p = parent[p] {
		chain = append(chain, p)
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return strings.Join(chain, " → ")
}
