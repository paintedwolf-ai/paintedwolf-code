package forbiddencalls

import (
	"go/ast"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// compositeIDParams contain "/" and ":", so they travel as one
// percent-encoded path segment.
var compositeIDParams = []string{"pack_id", "unit_id", "command_id", "meta_pack_id"}

var persistence = []laws.Rule{{
	// chi returns a raw path segment; composite ids are read with
	// httpio.EncodedPathID, the one reader that decodes them.
	Name:    "composite-id-path-params-are-read-decoded",
	Anchors: []laws.Anchor{laws.Func("internal/api/httpio", "EncodedPathID")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/api") {
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				if pkg != "github.com/go-chi/chi/v5" || name != "URLParam" || len(call.Args) != 2 {
					return
				}
				if param, ok := laws.StringLit(call.Args[1]); ok && contains(compositeIDParams, param) {
					r.ReportAt(call.Pos(), "chi.URLParam(r, %q) returns the raw segment; read it with httpio.EncodedPathID", param)
				}
			})
		}
	},
}, {
	// A mutation nobody observes cannot report that it stopped working: Exec
	// results are returned, logged or counted.
	Name:    "no-unobserved-database-mutations",
	Anchors: []laws.Anchor{laws.Package("internal/db")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, laws.Source...) {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok || !allBlank(assign.Lhs) || len(assign.Rhs) != 1 {
					return true
				}
				if call, ok := assign.Rhs[0].(*ast.CallExpr); ok {
					if name := laws.CalleeName(call); name == "Exec" || name == "ExecContext" {
						r.ReportAt(assign.Pos(), "%s result discarded; return it or hand it to a seam that counts and logs", name)
					}
				}
				return true
			})
		}
	},
}}

var serveWiring = []laws.Rule{{
	// A nil Live callback would let scheduled warming run without an active
	// app; the boards wiring sets it.
	Name:    "warm-runner-wired-with-presence-gate",
	Anchors: []laws.Anchor{laws.Decl("internal/webresearch", "WarmRunner.Live"), laws.GoPath("internal/app/boards/build.go")},
	Check: func(r *laws.Run) {
		f := r.GoFile("internal/app/boards/build.go")
		found := 0
		ast.Inspect(f.AST, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if pkg, name, ok := f.Qualified(lit.Type); ok && pkg == "github.com/lycaon/lycaon/internal/webresearch" && name == "WarmRunner" {
				found++
				if !setsField(lit, "Live") {
					r.ReportAt(lit.Pos(), "WarmRunner must set Live so scheduled warming runs only while the app is in use")
				}
			}
			return true
		})
		r.Expect(found, "webresearch.WarmRunner literals in internal/app/boards/build.go")
	},
}}

// allBlank reports an assignment whose every target is the blank identifier.
func allBlank(lhs []ast.Expr) bool {
	for _, e := range lhs {
		if id, ok := e.(*ast.Ident); !ok || id.Name != "_" {
			return false
		}
	}
	return len(lhs) > 0
}
