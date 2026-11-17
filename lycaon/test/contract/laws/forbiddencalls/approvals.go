package forbiddencalls

import (
	"go/ast"
	"go/types"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

var approvals = []laws.Rule{{
	// The approval face is computed only by plan construction, refacing and
	// the checkpoint wire projection.
	Name:    "recommended-option-set-in-plan-construction",
	Anchors: []laws.Anchor{laws.Decl("internal/hitl", "ApprovalPlan.RecommendedOptionID")},
	Exempt: []laws.Exemption{
		{Key: "internal/hitl/approval_plan.go", Reason: "plan construction"},
		{Key: "internal/hitl/checkpoint_wire.go", Reason: "checkpoint wire projection"},
		{Key: "internal/hitl/approval_offers.go:refaceAfterFilter", Reason: "refacing after the option filter"},
	},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				var at ast.Node
				switch node := n.(type) {
				case *ast.KeyValueExpr:
					if key, ok := node.Key.(*ast.Ident); ok && key.Name == "RecommendedOptionID" {
						at = node
					}
				case *ast.AssignStmt:
					for _, lhs := range node.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "RecommendedOptionID" {
							at = node
						}
					}
				}
				if at != nil && !r.Exempt(f.Rel) && !r.Exempt(f.Site(at.Pos())) {
					r.ReportAt(at.Pos(), "writes RecommendedOptionID outside approval plan construction")
				}
				return true
			})
		}
	},
}, {
	// A posture reaches host code only through gate.ParsePosture or decoding,
	// both of which refuse unknown tokens.
	Name:    "approval-postures-come-from-the-parser",
	Anchors: []laws.Anchor{laws.Decl("internal/gate", "Posture"), laws.Func("internal/gate", "ParsePosture")},
	Check: func(r *laws.Run) {
		const gate = "github.com/lycaon/lycaon/internal/gate"
		for _, f := range r.GoFiles(laws.Production, laws.Source...) {
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				if pkg == gate && name == "Posture" && len(call.Args) == 1 {
					r.ReportAt(call.Pos(), "gate.Posture(...) conversion carries an unchecked token; use gate.ParsePosture")
				}
			})
		}
	},
}}

var concurrency = []laws.Rule{{
	// Admission.deps is written by setDeps during a prompt turn while host
	// wake paths read it, so reads go through loopDeps under depsMu.
	Name: "loop-admission-deps-read-through-accessor",
	Anchors: []laws.Anchor{
		laws.Decl("internal/coordinator/loopwake", "Admission.deps"),
		laws.Func("internal/coordinator/loopwake", "Admission.loopDeps"),
		laws.Func("internal/coordinator/loopwake", "Admission.setDeps"),
	},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/coordinator/loopwake") {
			f.Funcs(func(fn *ast.FuncDecl) {
				if laws.ReceiverType(fn) != "Admission" || fn.Name.Name == "loopDeps" || fn.Name.Name == "setDeps" ||
					len(fn.Recv.List[0].Names) == 0 {
					return
				}
				recv := fn.Recv.List[0].Names[0].Name
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "deps" {
						if id, ok := sel.X.(*ast.Ident); ok && id.Name == recv {
							r.ReportAt(sel.Pos(), "%s reads %s.deps directly; snapshot through %s.loopDeps()", fn.Name.Name, recv, recv)
						}
					}
					return true
				})
			})
		}
	},
}}

var isolation = []laws.Rule{{
	// Isolation codes come from the outcome registry's constants.
	Name:    "isolation-codes-have-one-registry",
	Anchors: []laws.Anchor{laws.GoPath("internal/isolation/outcomes.go")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			if f.Rel == "internal/isolation/outcomes.go" {
				continue
			}
			laws.StringLits(f.AST, func(lit *ast.BasicLit, value string) {
				if strings.HasPrefix(value, "SANDBOX_") || strings.HasPrefix(value, "REMOTE_PACKAGE_EXECUTION_") {
					r.ReportAt(lit.Pos(), "isolation code %s outside internal/isolation/outcomes.go", value)
				}
			})
		}
	},
}, {
	// Approval verdicts stay silent handling or a human ask; the control
	// plane and human rules are the only hard denies.
	Name:    "approval-plane-has-no-system-deny",
	Anchors: []laws.Anchor{laws.Decl("internal/gate", "Verdict"), laws.Decl("internal/hitl", "ApprovalResult.Denied")},
	Exempt: []laws.Exemption{
		{Key: "internal/settings/rule_gate.go:deniedByRules", Reason: "a human policy rule denies"},
		{Key: "internal/settings/rule_gate.go:controlPlaneDenyResult", Reason: "the control plane is the one path-shaped hard deny"},
	},
	Check: func(r *laws.Run) {
		gate := r.TypedPackage("internal/gate").Types
		var verdicts []string
		for _, name := range gate.Scope().Names() {
			if c, ok := gate.Scope().Lookup(name).(*types.Const); ok {
				if named, ok := c.Type().(*types.Named); ok && named.Obj().Name() == "Verdict" && named.Obj().Pkg() == gate {
					verdicts = append(verdicts, name)
				}
			}
		}
		r.Diff("gate.Verdict constants", []string{"Ask", "Silent"}, verdicts)
		minted := map[string]int{}
		for _, f := range r.GoFiles(laws.Production, "internal") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || laws.TypeName(lit.Type) != "ApprovalResult" || !trueField(lit, "Denied") {
					return true
				}
				site := f.Site(lit.Pos())
				minted[site]++
				if !r.Exempt(site) {
					r.ReportAt(lit.Pos(), "ApprovalResult{Denied: true} outside human policy and control-plane enforcement")
				}
				return true
			})
		}
		for site, n := range minted {
			if n > 1 {
				r.Report("%s mints %d hard denies, want one", site, n)
			}
		}
	},
}}

// trueField reports a composite literal setting field to true.
func trueField(lit *ast.CompositeLit, field string) bool {
	for _, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == field {
				value, ok := kv.Value.(*ast.Ident)
				return ok && value.Name == "true"
			}
		}
	}
	return false
}
