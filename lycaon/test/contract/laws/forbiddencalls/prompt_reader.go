package forbiddencalls

import (
	"go/ast"
	"slices"

	"github.com/lycaon/lycaon/test/contract/laws"
)

const (
	sandboxPkg      = "github.com/lycaon/lycaon/internal/sandbox"
	toolcontractPkg = "github.com/lycaon/lycaon/internal/toolcontract"
)

var reachMarks = []string{"ReachCommandMutation", "ReachCommandExternalExec", "ReachCommandNetworkEgress"}

var promptReader = []laws.Rule{{
	// In internal/prompts only DeclaredReader loads tool profiles from disk.
	Name: "prompts-load-tool-profiles-through-declared-reader",
	Anchors: []laws.Anchor{
		laws.Func("internal/prompts", "DeclaredReader"),
		laws.Func("internal/sandbox", "LoadToolProfiles"),
		laws.Func("internal/sandbox", "LoadToolProfilesWithCatalog"),
	},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/prompts") {
			f.Funcs(func(fn *ast.FuncDecl) {
				if fn.Name.Name == "DeclaredReader" {
					return
				}
				f.Calls(fn.Body, func(call *ast.CallExpr, pkg, name string) {
					if pkg == sandboxPkg && (name == "LoadToolProfiles" || name == "LoadToolProfilesWithCatalog") {
						r.ReportAt(call.Pos(), "%s calls sandbox.%s; read through DeclaredReader", fn.Name.Name, name)
					}
				})
			})
		}
	},
}, {
	// Session and execution packages receive a reader; they never build one.
	Name:    "session-execution-never-calls-declared-reader",
	Anchors: []laws.Anchor{laws.Func("internal/prompts", "DeclaredReader")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.AnyGo, "internal/coordinator", "internal/session", "internal/app/sessions", "internal/toolexecution") {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "DeclaredReader" {
					r.ReportAt(call.Pos(), "calls DeclaredReader")
				}
			})
		}
	},
}, {
	// Reach marks are produced only by Reader.Held.
	Name: "reach-marks-produced-by-reader-held",
	Anchors: []laws.Anchor{
		laws.Decl("internal/toolcontract", "ReachMarks"),
		laws.Decl("internal/toolcontract", "ReachCommandMutation"),
		laws.Decl("internal/toolcontract", "ReachCommandExternalExec"),
		laws.Decl("internal/toolcontract", "ReachCommandNetworkEgress"),
	},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.AnyGo, "internal", "cmd", "pkg", "test") {
			if laws.Under(f, "internal/toolcontract", "internal/prompts") {
				continue
			}
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if pkg == toolcontractPkg && name == "ReachMarks" {
					r.ReportAt(sel.Pos(), "references toolcontract.ReachMarks")
				}
			})
			if f.IsTest {
				continue
			}
			ast.Inspect(f.AST, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, lhs := range assign.Lhs {
					if idx, ok := lhs.(*ast.IndexExpr); ok && isReachMark(f, idx.Index) {
						r.ReportAt(lhs.Pos(), "inserts a reach mark into a map")
					}
				}
				return true
			})
		}
	},
}}

func isReachMark(f *laws.GoFile, expr ast.Expr) bool {
	if pkg, name, ok := f.Qualified(expr); ok {
		return pkg == toolcontractPkg && slices.Contains(reachMarks, name)
	}
	id, ok := expr.(*ast.Ident)
	return ok && slices.Contains(reachMarks, id.Name)
}
