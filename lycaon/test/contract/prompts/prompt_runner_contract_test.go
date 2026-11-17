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

func TestWorkerPackageNoConcreteManager(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	workerDir := filepath.Join(root, "lycaon", "internal", "worker")
	fset := token.NewFileSet()
	var hits []string
	err := filepath.Walk(workerDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			star, ok := n.(*ast.StarExpr)
			if !ok {
				return true
			}
			sel, ok := star.X.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "session" {
				return true
			}
			if sel.Sel.Name == "Manager" {
				hits = append(hits, filepath.Base(path))
			}
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "operation failed", err)
	if len(hits) > 0 {
		t.Fatalf("internal/worker production code must not use *session.Manager; found in: %v", hits)
	}
}

func TestDelegationPackageNoConcreteManagerForSpawn(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	retiredDir := filepath.Join(root, "lycaon", "internal", "delegation")
	if _, err := os.Stat(retiredDir); !os.IsNotExist(err) {
		t.Fatalf("retired internal/delegation package still exists: err=%v", err)
	}
	for _, pkg := range []string{"workerplan", "workertopology"} {
		files, err := filepath.Glob(filepath.Join(root, "lycaon", "internal", pkg, "*.go"))
		contractcheck.FailErr(t, "glob "+pkg, err)
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			data, err := os.ReadFile(path)
			contractcheck.FailErr(t, "read "+pkg, err)
			src := string(data)
			if strings.Contains(src, "SpawnChild") || strings.Contains(src, ".Prompt(") {
				t.Fatalf("%s must not call SpawnChild or Prompt on Sessions directly", filepath.Base(path))
			}
		}
	}
}
