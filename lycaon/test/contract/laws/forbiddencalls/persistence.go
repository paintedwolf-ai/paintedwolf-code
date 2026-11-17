package forbiddencalls

import (
	"go/ast"
	"regexp"
	"strings"

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

var projectFiles = []laws.Rule{{
	// AGENTS.md changes go through the reviewed file-change path.
	Name:    "no-hardcoded-agents-md-writes",
	Anchors: []laws.Anchor{laws.Package("internal/fseffect")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				if laws.CalleeName(call) != "WriteFile" || len(call.Args) == 0 {
					return
				}
				if target, ok := laws.StringLit(call.Args[0]); ok && strings.Contains(target, "AGENTS.md") {
					r.ReportAt(call.Pos(), "writes AGENTS.md outside the reviewed file-change path")
				}
			})
		}
	},
}, {
	// Source recording receives its history, command and checkpoint ports
	// explicitly; it never recovers them from the recorder by assertion.
	Name: "source-recording-port-recovers-no-peer-capabilities",
	Anchors: []laws.Anchor{laws.AnchorFunc("a SourceLedger field", func(c *laws.Corpus) error {
		return anyProductionSelector(c, "internal", "SourceLedger")
	})},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			f.Funcs(func(fn *ast.FuncDecl) {
				for _, assertion := range sourceRecorderAssertions(fn.Body) {
					r.ReportAt(assertion.Pos(), "type-asserts the source recorder; take the port explicitly")
				}
			})
		}
	},
}}

func sourceRecorderAssertions(body *ast.BlockStmt) []*ast.TypeAssertExpr {
	aliases := map[string]bool{}
	var isRecorder func(ast.Expr) bool
	isRecorder = func(expression ast.Expr) bool {
		switch expression := expression.(type) {
		case *ast.Ident:
			return expression.Name == "sourceLedger" || aliases[expression.Name]
		case *ast.SelectorExpr:
			return expression.Sel.Name == "SourceLedger" || expression.Sel.Name == "sourceLedger"
		case *ast.ParenExpr:
			return isRecorder(expression.X)
		}
		return false
	}
	for changed := true; changed; {
		changed = false
		ast.Inspect(body, func(node ast.Node) bool {
			assignment, ok := node.(*ast.AssignStmt)
			if !ok || len(assignment.Lhs) != len(assignment.Rhs) {
				return true
			}
			for index, expression := range assignment.Rhs {
				name, ok := assignment.Lhs[index].(*ast.Ident)
				if ok && isRecorder(expression) && !aliases[name.Name] {
					aliases[name.Name] = true
					changed = true
				}
			}
			return true
		})
	}
	var assertions []*ast.TypeAssertExpr
	ast.Inspect(body, func(node ast.Node) bool {
		assertion, ok := node.(*ast.TypeAssertExpr)
		if ok && isRecorder(assertion.X) {
			assertions = append(assertions, assertion)
		}
		return true
	})
	return assertions
}

var snakeCaseField = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

var wireQueries = []laws.Rule{{
	// A query diagnostic names the one snake_case field it refuses.
	Name:    "api-query-diagnostics-name-one-field",
	Anchors: []laws.Anchor{laws.Named("internal/api", "InvalidQueryParam")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/api") {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "InvalidQueryParam" || len(call.Args) < 2 {
					return
				}
				if lit, ok := call.Args[1].(*ast.BasicLit); ok {
					if field, ok := laws.StringLit(lit); !ok || !snakeCaseField.MatchString(field) {
						r.ReportAt(lit.Pos(), "query diagnostic must identify one snake_case field, got %s", lit.Value)
					}
				}
			})
		}
	},
}, {
	// Handlers read project_id and session_id, never the retired keys.
	Name:    "api-handlers-use-no-forbidden-query-keys",
	Anchors: []laws.Anchor{laws.Package("internal/api")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/api") {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Get" || len(call.Args) != 1 {
					return
				}
				query, ok := sel.X.(*ast.CallExpr)
				if !ok || laws.CalleeName(query) != "Query" {
					return
				}
				if key, ok := laws.StringLit(call.Args[0]); ok && contains([]string{"project", "project_dir", "session"}, key) {
					r.ReportAt(call.Pos(), "URL.Query().Get(%q); use project_id or session_id", key)
				}
			})
		}
	},
}}
