package contract

import (
	"go/ast"
)

func isFilepathBaseCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if pkg, isIdent := sel.X.(*ast.Ident); isIdent && pkg.Name == "filepath" && sel.Sel.Name == "Base" {
		return true
	}
	// A wrapper — strings.ToLower, strings.TrimSpace — leaves the tag a program name.
	for _, arg := range call.Args {
		if isFilepathBaseCall(arg) {
			return true
		}
	}
	return false
}
