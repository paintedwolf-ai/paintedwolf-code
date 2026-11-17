package contract

import (
	"go/ast"
	"go/token"
	"strconv"
)

func literals(body *ast.BlockStmt) map[string]bool {
	found := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if v, err := strconv.Unquote(lit.Value); err == nil {
			found[v] = true
		}
		return true
	})
	return found
}
