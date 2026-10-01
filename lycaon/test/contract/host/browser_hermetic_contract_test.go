package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Browser resolution stays pinned across production and test files.

func browserPackageFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	files := map[string]*ast.File{}
	for _, pkg := range []string{"browser", "browserengine"} {
		collectGoFiles(t, filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", pkg), files)
	}
	return files
}

// collectGoFiles parses every Go file under root, test files included.
func collectGoFiles(t *testing.T, root string, into map[string]*ast.File) {
	t.Helper()
	found := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		parsed, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return perr
		}
		into[path] = parsed
		found++
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if found == 0 {
		t.Fatalf("no Go files found under %s; layout changed", root)
	}
}

// The browser test gate stays below its launcher to avoid an import cycle.
func TestBrowserTestGateStaysBelowTheLauncher(t *testing.T) {
	t.Parallel()
	const launcher = "github.com/lycaon/lycaon/internal/browser"
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "browserengine", "browsertest")
	files := map[string]*ast.File{}
	collectGoFiles(t, dir, files)
	for path, file := range files {
		for _, imp := range file.Imports {
			if imp.Path != nil && strings.Trim(imp.Path.Value, `"`) == launcher {
				t.Errorf("%s imports %s: the gate must stay below the launcher so one gate serves every caller",
					relToRepo(t, path), launcher)
			}
		}
	}
}

// The browser stays in its launcher's process group for parent-exit cleanup.
func TestBrowserKeepsItsLauncherProcessGroup(t *testing.T) {
	t.Parallel()
	for path, file := range browserPackageFiles(t) {
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "Setpgid", "Pdeathsig", "CREATE_NEW_PROCESS_GROUP":
				t.Errorf("%s sets %s: chrome stays in its launcher's group so a hard-killed "+
					"parent's group sweep reaches it; its own children need no reaping",
					relToRepo(t, path), sel.Sel.Name)
			}
			return true
		})
	}
}

// Browser resolution never searches PATH.
func TestBrowserNeverResolvesFromPATH(t *testing.T) {
	t.Parallel()
	for path, file := range browserPackageFiles(t) {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if name := contractcheck.CallName(call); strings.HasSuffix(name, "LookPath") {
				t.Errorf("%s calls %s: the browser resolves env → bundle → managed cache "+
					"and refuses when none is present; it never searches PATH",
					relToRepo(t, path), name)
			}
			return true
		})
	}
}

func relToRepo(t *testing.T, path string) string {
	t.Helper()
	rel, err := filepath.Rel(contractcheck.RepoRoot(t), path)
	if err != nil {
		return path
	}
	return rel
}
