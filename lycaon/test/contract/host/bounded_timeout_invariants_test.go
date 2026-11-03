// Functions named by contextcheck suppressions that claim an internal timeout
// call context.WithTimeout or context.WithDeadline, directly or through a helper.

package contract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type timeoutClaim struct {
	label    string // human-readable
	pkgRel   string // path relative to lycaon module root
	receiver string // type name for methods; "" for top-level
	funcName string
}

var timeoutClaims = []timeoutClaim{
	{"gitRemoteHash", "internal/project", "", "gitRemoteHash"},
	{"(*sqlQueueCore).Get", "internal/worker", "sqlQueueCore", "Get"},
	{"fetchDiscoveredModels", "internal/llm", "", "fetchDiscoveredModels"},
}

func TestBoundedTimeoutClaimsConstructDeadline(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, claim := range timeoutClaims {
		t.Run(claim.label, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(root, "lycaon", claim.pkgRel)
			fset, files := contractcheck.ParseNonTestGoTree(t, dir)
			var fn *ast.FuncDecl
			for _, file := range files {
				if filepath.Dir(fset.Position(file.Pos()).Filename) != dir {
					continue
				}
				if candidate := findFuncOrMethod(file, claim.receiver, claim.funcName); candidate != nil {
					if fn != nil {
						t.Fatalf("ambiguous timeout claim %s in %s", claim.label, dir)
					}
					fn = candidate
				}
			}
			if fn == nil {
				t.Fatalf("could not find %s in %s", claim.label, dir)
			}
			if !bodyConstructsDeadline(fn.Body) {
				t.Fatalf(
					"%s claims to be bounded by an internal timeout, but its body never calls\n"+
						"context.WithTimeout or context.WithDeadline. Either:\n"+
						"  1. Add a real bound (context.WithTimeout(ctx, …) + defer cancel()).\n"+
						"  2. Or update the //nolint:contextcheck comment to drop the timeout claim\n"+
						"     and explain why caller cancellation isn't needed.",
					claim.label,
				)
			}
		})
	}
}

func findFuncOrMethod(f *ast.File, receiver, name string) *ast.FuncDecl {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name {
			continue
		}
		if receiver == "" {
			if fn.Recv == nil {
				return fn
			}
			continue
		}
		if fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		rt := fn.Recv.List[0].Type
		if star, ok := rt.(*ast.StarExpr); ok {
			rt = star.X
		}
		if ident, ok := rt.(*ast.Ident); ok && ident.Name == receiver {
			return fn
		}
	}
	return nil
}

func bodyConstructsDeadline(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "context" {
			return true
		}
		if sel.Sel.Name == "WithTimeout" || sel.Sel.Name == "WithDeadline" {
			found = true
			return false
		}
		return true
	})
	return found
}

// The detector matches a body that constructs a timeout and rejects one that
// does not.
func TestBoundedTimeoutDetectorSelfCheck(t *testing.T) {
	t.Parallel()
	src := `package p
import "context"
func With() { ctx, cancel := context.WithTimeout(context.Background(), 0); defer cancel(); _ = ctx }
func Without() { _ = "no timeout here" }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	contractcheck.FailErr(t, "parse synthetic source", err)
	with := findFuncOrMethod(f, "", "With")
	without := findFuncOrMethod(f, "", "Without")
	if !bodyConstructsDeadline(with.Body) {
		t.Fatal("detector missed a real context.WithTimeout call")
	}
	if bodyConstructsDeadline(without.Body) {
		t.Fatal("detector falsely flagged a body with no context call")
	}
	// Catch the "any function name matches" failure mode.
	if findFuncOrMethod(f, "", "Nonexistent") != nil {
		t.Fatal(fmt.Errorf("findFuncOrMethod returned non-nil for a missing function"))
	}
}
