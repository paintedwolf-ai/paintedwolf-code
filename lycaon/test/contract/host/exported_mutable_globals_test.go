package contract

import (
	"go/ast"
)

// compositeKind reports "slice/array" or "map" for a slice/array/map type or composite
// literal, and "" for anything else (scalars, sentinel errors, regexps, structs).
func compositeKind(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.ArrayType:
		return "slice/array"
	case *ast.MapType:
		return "map"
	case *ast.CompositeLit:
		return compositeKind(t.Type)
	}
	return ""
}
