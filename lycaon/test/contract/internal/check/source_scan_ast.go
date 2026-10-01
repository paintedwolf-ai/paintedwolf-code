package check

import (
	"go/ast"
	"go/token"
	"strings"
)

// AstStringLit returns a string literal's value.
func AstStringLit(expr ast.Expr) string {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	return strings.Trim(lit.Value, "`\"")
}

// ImportAliasFor returns an import's explicit or path-derived local name.
func ImportAliasFor(file *ast.File, pkgPath string) string {
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, "`\"")
		if path != pkgPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		if i := strings.LastIndex(path, "/"); i >= 0 {
			return path[i+1:]
		}
		return path
	}
	return ""
}
