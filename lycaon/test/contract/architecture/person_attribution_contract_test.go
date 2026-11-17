package contract

import (
	"go/ast"
	"path/filepath"
	"slices"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const peoplePkg = "github.com/lycaon/lycaon/internal/people"

// agentInvocationEntries are the executor doors through which a model's call
// reaches an owner; each enters through the shared invocation context.
var agentInvocationEntries = []string{"Invoke", "InvokeNative", "ApplyProgramProgress"}

func TestAgentToolCallsCarryNoRequestCaller(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corp, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon", "internal", "toolexecution"))
	contractcheck.FailErr(t, "load AST corpus", err)
	stripped := false
	entries := map[string]bool{}
	for _, gf := range corp.Files() {
		if gf.IsTest {
			continue
		}
		alias := contractcheck.ImportAliasFor(gf.AST, peoplePkg)
		for _, fn := range funcDecls(gf.AST) {
			if fn.Name.Name == "invocationContext" && fn.Recv == nil {
				stripped = alias != "" && callsSelector(fn.Body, alias, "WithoutCaller")
				continue
			}
			if receiverName(fn) == "Executor" && slices.Contains(agentInvocationEntries, fn.Name.Name) {
				entries[fn.Name.Name] = callsFunction(fn.Body, "invocationContext")
			}
		}
	}
	if !stripped {
		t.Error("invocationContext must strip the request caller with people.WithoutCaller " +
			"so agent effects are never recorded as a person's")
	}
	for _, name := range agentInvocationEntries {
		if ok, found := entries[name]; !found {
			t.Errorf("Executor.%s not found", name)
		} else if !ok {
			t.Errorf("Executor.%s must enter through invocationContext, which strips the request caller", name)
		}
	}
}

func callsFunction(body ast.Node, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == name {
				found = true
			}
		}
		return !found
	})
	return found
}

func funcDecls(f *ast.File) []*ast.FuncDecl {
	var out []*ast.FuncDecl
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			out = append(out, fn)
		}
	}
	return out
}

func receiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// callsSelector reports a call to pkg.name inside body.
func callsSelector(body ast.Node, pkg, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return !found
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == pkg {
				found = true
			}
		}
		return !found
	})
	return found
}
