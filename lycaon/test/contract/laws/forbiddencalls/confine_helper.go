package forbiddencalls

import (
	"go/ast"
	"go/build/constraint"

	"github.com/lycaon/lycaon/test/contract/laws"
)

var confineHelper = []laws.Rule{{
	// The confinement helper dispatches from init in every build but the
	// helper's own coverage main, is not exported, and refuses nesting.
	Name:    "helper-dispatch-is-confine-init",
	Anchors: []laws.Anchor{laws.Func("internal/confine", "runHelperIfInvoked"), laws.Func("internal/confine", "Command")},
	Check: func(r *laws.Run) {
		initDispatches := false
		for _, f := range r.GoFiles(laws.Production, "internal/confine") {
			if f.Dir != "internal/confine" || onlyInHelperMainBuild(f.AST) {
				continue
			}
			f.Funcs(func(fn *ast.FuncDecl) {
				switch {
				case fn.Name.Name == "RunHelperIfInvoked":
					r.ReportAt(fn.Pos(), "RunHelperIfInvoked is exported; dispatch stays inside confine's init")
				case fn.Name.Name == "init" && fn.Recv == nil && mentions(fn.Body, "runHelperIfInvoked"):
					initDispatches = true
				}
			})
		}
		if !initDispatches {
			r.Report("no init in internal/confine calls runHelperIfInvoked")
		}
		command := r.FuncDecl("internal/confine", "Command")
		if !mentions(command.Body, "IsHelperInvocation") || !mentions(command.Body, "ErrNestedHelper") {
			r.ReportAt(command.Pos(), "Command must refuse IsHelperInvocation with ErrNestedHelper")
		}
	},
}, {
	// Automatic confinement is a production opt-in; tests confine explicitly.
	Name:    "enable-auto-confine-is-production-opt-in",
	Anchors: []laws.Anchor{laws.Func("internal/confine", "EnableAutoConfine")},
	Check: func(r *laws.Run) {
		production := 0
		for _, f := range r.GoFiles(laws.AnyGo, "internal", "cmd", "pkg", "config", "test") {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				if laws.CalleeName(call) != "EnableAutoConfine" {
					return
				}
				if f.IsTest {
					r.ReportAt(call.Pos(), "a test enables automatic confinement")
				} else {
					production++
				}
			})
		}
		r.Expect(production, "production EnableAutoConfine calls")
	},
}}

// onlyInHelperMainBuild reports a file compiled only under confinehelpermain,
// the helper's coverage build, which dispatches from its own main.
func onlyInHelperMainBuild(f *ast.File) bool {
	for _, group := range f.Comments {
		if group.Pos() >= f.Package {
			break
		}
		for _, c := range group.List {
			if expr, err := constraint.Parse(c.Text); err == nil {
				return requiresTag(expr, "confinehelpermain")
			}
		}
	}
	return false
}

func requiresTag(e constraint.Expr, tag string) bool {
	switch x := e.(type) {
	case *constraint.TagExpr:
		return x.Tag == tag
	case *constraint.AndExpr:
		return requiresTag(x.X, tag) || requiresTag(x.Y, tag)
	}
	return false
}

// mentions reports an identifier named name below node.
func mentions(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		found = found || ok && id.Name == name
		return !found
	})
	return found
}
