package contract

// AST scan for silent-invalidator invariant over the stable-prefix
// code surface defined in docs/session.md.

import (
	"go/ast"
)

func selectorPackage(x ast.Expr) string {
	switch v := x.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return selectorPackage(v.X) + "." + v.Sel.Name
	default:
		return ""
	}
}
