package sqlschema

import (
	"go/ast"
	"go/token"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

var queryRules = []laws.Rule{
	{Name: "production-go-does-not-return-err-no-rows", Anchors: []laws.Anchor{noRowsDoor}, Check: doesNotReturnErrNoRows},
	{Name: "err-no-rows-is-matched-with-errors-is", Anchors: []laws.Anchor{noRowsDoor}, Check: errNoRowsMatchedWithErrorsIs},
	{Name: "production-sql-uses-context-apis", Anchors: []laws.Anchor{laws.Package("internal/db")}, Check: productionSQLUsesContextAPIs},
	{Name: "no-sql-string-interpolation", Anchors: []laws.Anchor{laws.Func("internal/db", "INPlaceholders")}, Check: noSQLStringInterpolation},
	{Name: "store-layer-has-no-hand-written-sql", Anchors: []laws.Anchor{laws.Package("internal/db")}, Check: storeLayerHasNoHandWrittenSQL},
}

var noRowsDoor = laws.Func("internal/db", "IsNoRows")

// production lists the production Go of internal/ outside sqlc output.
func production(r *laws.Run) []*laws.GoFile {
	return r.GoFiles(func(f *laws.GoFile) bool { return !f.IsTest && !strings.HasSuffix(f.Rel, ".sql.go") }, "internal")
}

// A store maps sql.ErrNoRows to a typed domain error or (nil, nil).
func doesNotReturnErrNoRows(r *laws.Run) {
	for _, f := range production(r) {
		ast.Inspect(f.AST, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			for _, res := range ret.Results {
				if pkg, name, ok := f.Qualified(res); ok && pkg == "database/sql" && name == "ErrNoRows" {
					r.ReportAt(res.Pos(), "returns sql.ErrNoRows; map it to a typed domain error or (nil, nil)")
				}
			}
			return true
		})
	}
}

// Wrapped errors hide ErrNoRows from ==; match it with errors.Is or
// db.IsNoRows.
func errNoRowsMatchedWithErrorsIs(r *laws.Run) {
	for _, f := range production(r) {
		ast.Inspect(f.AST, func(n ast.Node) bool {
			bin, ok := n.(*ast.BinaryExpr)
			if !ok || bin.Op != token.EQL && bin.Op != token.NEQ {
				return true
			}
			for _, side := range []ast.Expr{bin.X, bin.Y} {
				if pkg, name, ok := f.Qualified(side); ok && pkg == "database/sql" && name == "ErrNoRows" {
					r.ReportAt(bin.Pos(), "compares with sql.ErrNoRows; use errors.Is(err, sql.ErrNoRows) or db.IsNoRows(err)")
				}
			}
			return true
		})
	}
}

// Request-path SQL passes its context: a literal SQL string goes to
// ExecContext, QueryContext or QueryRowContext.
func productionSQLUsesContextAPIs(r *laws.Run) {
	for _, f := range production(r) {
		f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || len(call.Args) == 0 || pkg == "syscall" {
				return
			}
			switch sel.Sel.Name {
			case "Exec", "Query", "QueryRow":
			default:
				return
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				r.ReportAt(call.Pos(), "use %sContext(ctx, …) instead of %s", sel.Sel.Name, sel.Sel.Name)
			}
		})
	}
}

var sqlInFormat = regexp.MustCompile(`(?i)(select\s|insert\s+into|update\s|delete\s+from|\swhere\s|\svalues\s*\()`)

// SQL is never built with fmt.Sprintf: dynamic shape comes from sqlc
// toggles, or from constant fragments plus db.INPlaceholders with bound
// parameters.
func noSQLStringInterpolation(r *laws.Run) {
	for _, f := range production(r) {
		f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
			if pkg != "fmt" || name != "Sprintf" || len(call.Args) == 0 {
				return
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING && sqlInFormat.MatchString(lit.Value) {
				r.ReportAt(call.Pos(), "fmt.Sprintf builds SQL; bind values with ? placeholders (db.INPlaceholders for IN lists)")
			}
		})
	}
}

// storePackages persist through generated sqlc query methods.
var storePackages = []string{
	"internal/call",
	"internal/findings",
	"internal/hitl",
	"internal/progress",
	"internal/scan",
	"internal/session/store",
	"internal/worker",
	"internal/workflow",
}

// A store package passes generated query identifiers, never inline SQL.
func storeLayerHasNoHandWrittenSQL(r *laws.Run) {
	for _, f := range r.GoFiles(laws.Production, storePackages...) {
		f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || len(call.Args) == 0 {
				return
			}
			switch sel.Sel.Name {
			case "ExecContext", "QueryContext", "QueryRowContext":
			default:
				return
			}
			query := call.Args[len(call.Args)-1]
			if len(call.Args) >= 2 {
				query = call.Args[1]
			}
			if lit, ok := query.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				r.ReportAt(call.Pos(), "inline SQL in a store package; add the query to internal/db/queries and call the generated method")
			}
		})
	}
}
