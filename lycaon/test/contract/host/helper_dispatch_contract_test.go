package contract

import (
	"go/build/constraint"
)

func requiresTag(e constraint.Expr, tag string) bool {
	switch x := e.(type) {
	case *constraint.TagExpr:
		return x.Tag == tag
	case *constraint.AndExpr:
		return requiresTag(x.X, tag) || requiresTag(x.Y, tag)
	}
	return false
}
