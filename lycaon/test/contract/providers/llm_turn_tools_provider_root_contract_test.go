package contract

import (
	"go/ast"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestLLMTurnToolAssemblyAlwaysTrimsFunctionRoots walks coordinatorToolsForTurn
// and fails if any non-nil return can carry a ToolMeta that never passed the
// provider-root trim. Catalog schemas may keep root anyOf; the assembly seam
// trims them.
func TestLLMTurnToolAssemblyAlwaysTrimsFunctionRoots(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "coordinator", "promptloop")
	fn := findFuncDeclInDir(t, dir, "coordinatorToolsForTurn")
	tainted := trimTaintedIdents(fn)
	for _, ret := range firstReturnExprs(fn) {
		if returnIsNil(ret) {
			continue
		}
		if isProviderRootTrimCall(ret) {
			continue
		}
		ident, ok := ret.(*ast.Ident)
		if !ok {
			t.Fatalf("coordinatorToolsForTurn returns %#T; add a trim-taint case or return a trimmed ident", ret)
		}
		if !tainted[ident.Name] {
			t.Fatalf("coordinatorToolsForTurn returns %s without a provider-root trim", ident.Name)
		}
	}
}

// TestBuildTurnRequestIsTheOnlyPromptloopToolsSeam locks the single assignment
// of CompletionRequest.Tools in the prompt loop and requires the function-root
// validator to run before the request leaves the host.
func TestBuildTurnRequestIsTheOnlyPromptloopToolsSeam(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "coordinator", "promptloop")
	fset, files := contractcheck.ParseNonTestGoTree(t, dir)
	var assigns []string
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range assign.Lhs {
				if !isToolsField(lhs) {
					continue
				}
				pos := fset.Position(lhs.Pos())
				rhs := ""
				if i < len(assign.Rhs) {
					rhs = exprIdentName(assign.Rhs[i])
				}
				assigns = append(assigns, pos.Filename+":"+strconv.Itoa(pos.Line)+"="+rhs)
			}
			return true
		})
	}
	if len(assigns) != 1 {
		t.Fatalf("promptloop must assign CompletionRequest.Tools in exactly one place, got %v", assigns)
	}
	if !strings.HasSuffix(assigns[0], "=turnTools") {
		t.Fatalf("Tools assignment = %s, want turnTools from coordinatorToolsForTurn", assigns[0])
	}
	build := findFuncDeclInDir(t, dir, "buildTurnRequest")
	if !funcCallsNamed(build, "validateTurnToolFunctionRoots") && !funcCallsNamed(build, "ValidateFunctionParametersRoot") {
		t.Fatal("buildTurnRequest must validate function-parameter roots before the provider call")
	}
}

func findFuncDeclInDir(t *testing.T, dir, name string) *ast.FuncDecl {
	t.Helper()
	_, files := contractcheck.ParseNonTestGoTree(t, dir)
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Name.Name != name || fn.Body == nil {
				continue
			}
			return fn
		}
	}
	t.Fatalf("function %s not found in %s", name, dir)
	return nil
}

func firstReturnExprs(fn *ast.FuncDecl) []ast.Expr {
	var out []ast.Expr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 {
			return true
		}
		out = append(out, ret.Results[0])
		return true
	})
	return out
}

func returnIsNil(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "nil"
}

func isProviderRootTrimCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	return isProviderRootTrimName(contractcheck.CallName(call))
}

func isProviderRootTrimName(name string) bool {
	base := name
	if i := strings.LastIndex(name, "."); i >= 0 {
		base = name[i+1:]
	}
	lower := strings.ToLower(base)
	return strings.Contains(lower, "trim") && strings.Contains(lower, "toolmeta")
}

func trimTaintedIdents(fn *ast.FuncDecl) map[string]bool {
	tainted := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.AssignStmt:
			applyAssignTaint(tainted, stmt)
		case *ast.DeclStmt:
			gen, ok := stmt.Decl.(*ast.GenDecl)
			if !ok {
				return true
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if name == nil {
						continue
					}
					if i < len(vs.Values) && exprIsTrimTainted(tainted, vs.Values[i]) {
						tainted[name.Name] = true
					}
				}
			}
		}
		return true
	})
	return tainted
}

func applyAssignTaint(tainted map[string]bool, stmt *ast.AssignStmt) {
	if isAppendAssign(stmt) {
		if dest, ok := stmt.Lhs[0].(*ast.Ident); ok {
			elems := appendElems(stmt.Rhs[0])
			allTainted := true
			for _, elem := range elems {
				if !exprIsTrimTainted(tainted, elem) {
					allTainted = false
					break
				}
			}
			tainted[dest.Name] = allTainted && (tainted[dest.Name] || len(elems) == 0)
		}
		return
	}
	for i, lhs := range stmt.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok || ident.Name == "_" || i >= len(stmt.Rhs) {
			continue
		}
		tainted[ident.Name] = exprIsTrimTainted(tainted, stmt.Rhs[i])
	}
}

func exprIsTrimTainted(tainted map[string]bool, expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return tainted[e.Name]
	case *ast.CallExpr:
		if isProviderRootTrimName(contractcheck.CallName(e)) || contractcheck.CallName(e) == "make" {
			return true
		}
		for _, arg := range e.Args {
			if exprIsTrimTainted(tainted, arg) {
				return true
			}
		}
	}
	return false
}

func isAppendAssign(stmt *ast.AssignStmt) bool {
	if len(stmt.Rhs) != 1 {
		return false
	}
	call, ok := stmt.Rhs[0].(*ast.CallExpr)
	return ok && contractcheck.CallName(call) == "append"
}

func appendElems(expr ast.Expr) []ast.Expr {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) < 2 {
		return nil
	}
	return call.Args[1:]
}

func isToolsField(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Tools" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "req"
}

func exprIdentName(expr ast.Expr) string {
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return ""
	}
	return ident.Name
}

func funcCallsNamed(fn *ast.FuncDecl, name string) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		got := contractcheck.CallName(call)
		if got == name || strings.HasSuffix(got, "."+name) {
			found = true
		}
		return true
	})
	return found
}
