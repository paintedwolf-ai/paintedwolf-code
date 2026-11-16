package forbiddencalls

import (
	"go/ast"

	"github.com/lycaon/lycaon/test/contract/laws"
)

var testInfrastructure = []laws.Rule{{
	// Leak teardown and its ignore list are shared through one seam.
	Name:    "goleak-through-testutil",
	Anchors: []laws.Anchor{laws.Func("internal/testutil", "VerifyNoLeaks")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.AnyGo, "internal", "cmd", "pkg", "test") {
			if laws.Under(f, "internal/testutil") {
				continue
			}
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if pkg == "go.uber.org/goleak" {
					r.ReportAt(sel.Pos(), "goleak.%s outside testutil.VerifyNoLeaks", name)
				}
			})
		}
	},
}}
