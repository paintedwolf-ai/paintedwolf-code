package contract

import (
	"go/ast"
	"go/token"
	"strings"
)

// retentionStoreType reports supported keyed store types.
func retentionStoreType(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.MapType:
		return true
	case *ast.StarExpr:
		return retentionStoreType(t.X)
	case *ast.SelectorExpr:
		pkg, ok := t.X.(*ast.Ident)
		if !ok {
			return false
		}
		return (pkg.Name == "sync" && t.Sel.Name == "Map") ||
			(pkg.Name == "scopedstore" && (t.Sel.Name == "LRU" || t.Sel.Name == "Map"))
	case *ast.IndexExpr:
		return retentionStoreType(t.X)
	}
	return false
}

func unwrapSelector(expr ast.Expr) (string, bool) {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return unwrapSelector(t.X)
	case *ast.IndexExpr:
		return unwrapSelector(t.X)
	case *ast.SelectorExpr:
		if pkg, ok := t.X.(*ast.Ident); ok {
			return pkg.Name + "." + t.Sel.Name, true
		}
	}
	return "", false
}

func itoaLine(fset *token.FileSet, pos token.Pos) string {
	p := fset.Position(pos)
	return strings.TrimSpace(strings.TrimPrefix(p.String(), p.Filename+":"))
}
