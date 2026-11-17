package forbiddencalls

import (
	"go/ast"

	"github.com/lycaon/lycaon/test/contract/laws"
)

const anchorPkg = "github.com/lycaon/lycaon/internal/coordinator/anchor"

var coordinator = []laws.Rule{{
	// A match context carries a workflow only through anchor.RunMatch, which
	// binds the workflow version.
	Name:    "match-context-workflow-through-run-match",
	Anchors: []laws.Anchor{laws.Decl("internal/coordinator/anchor", "MatchContext.Workflow"), laws.Func("internal/coordinator/anchor", "RunMatch")},
	Exempt:  []laws.Exemption{{Key: "internal/coordinator/anchor/binding.go:RunMatch", Reason: "the binding constructor itself"}},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isMatchContext(f, lit.Type) || !setsField(lit, "Workflow") || r.Exempt(f.Site(lit.Pos())) {
					return true
				}
				r.ReportAt(lit.Pos(), "builds MatchContext{Workflow: ...}; use anchor.RunMatch to bind the workflow version")
				return true
			})
		}
	},
}}

func isMatchContext(f *laws.GoFile, typ ast.Expr) bool {
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name == "MatchContext" && f.Pkg == anchorPkg
	}
	pkg, name, ok := f.Qualified(typ)
	return ok && pkg == anchorPkg && name == "MatchContext"
}

// setsField reports a keyed composite literal element for field.
func setsField(lit *ast.CompositeLit, field string) bool {
	for _, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == field {
				return true
			}
		}
	}
	return false
}
