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
	delegationDir := filepath.Join(root, "lycaon", "internal", "delegation")
	data, err := os.ReadFile(filepath.Join(delegationDir, "manager_impl.go"))
	contractcheck.FailErr(t, "read file", err)
	src := string(data)
	if strings.Contains(src, "SpawnChild") || strings.Contains(src, ".Prompt(") {
		t.Fatalf("delegation should not call spawn/prompt on Sessions directly")
	}
}
