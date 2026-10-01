package contract

import "go/ast"

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if f.Sel != nil {
			return f.Sel.Name
		}
	}
	return ""
}
