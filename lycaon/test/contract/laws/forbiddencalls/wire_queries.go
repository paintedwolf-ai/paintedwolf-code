package forbiddencalls

import (
	"go/ast"
	"regexp"

	"github.com/lycaon/lycaon/test/contract/laws"
)

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
