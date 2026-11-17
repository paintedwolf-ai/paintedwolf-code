package forbiddencalls

import (
	"go/ast"
	"go/token"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// goroutineShutdownPackages hold the serving goroutines that shutdown joins.
var goroutineShutdownPackages = []string{"internal/app", "internal/session", "internal/app/security", "internal/session/turnadmission"}

var globals = []laws.Rule{{
	// Shipped code exports no slice or map global every importer could
	// mutate; composites are exposed through copy or lookup functions.
	Name:    "no-exported-mutable-slice-or-map-globals",
	Anchors: []laws.Anchor{laws.Package("internal/app")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal", "cmd", "pkg", "config") {
			for _, decl := range f.AST.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.VAR {
					continue
				}
				for _, spec := range gen.Specs {
					value := spec.(*ast.ValueSpec)
					for i, name := range value.Names {
						if !name.IsExported() {
							continue
						}
						kind := compositeKind(value.Type)
						if kind == "" && i < len(value.Values) {
							kind = compositeKind(value.Values[i])
						}
						if kind != "" {
							r.ReportAt(name.Pos(), "exports the mutable %s global %s; unexport it behind a copy or lookup", kind, name.Name)
						}
					}
				}
			}
		}
	},
}, {
	// A background goroutine in the serving packages is registered with the
	// wait group shutdown joins.
	Name:    "background-goroutines-pair-with-shutdown",
	Anchors: []laws.Anchor{laws.Package("internal/app"), laws.Package("internal/session/turnadmission")},
	Exempt: []laws.Exemption{
		{Key: "internal/app/runners.go", Reason: "startRunners pairs wg.Add with runner goroutines"},
		{Key: "internal/app/serve.go", Reason: "Run uses http.Server.Shutdown for the listen goroutine"},
		{Key: "internal/app/parent_watch.go", Reason: "Run cancels the parent watch and joins its channel before draining"},
		{Key: "internal/app/security/evidence.go", Reason: "the secret-harvest sweep is an event callback the bound tree sweep coalesces and finishes"},
		{Key: "internal/session/turnadmission/queue_round_drain.go", Reason: "round-end drains register with roundEndDrains, which Service.Wait cancels and joins"},
	},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, goroutineShutdownPackages...) {
			if !contains(goroutineShutdownPackages, f.Dir) {
				continue
			}
			f.Funcs(func(fn *ast.FuncDecl) {
				if addsOneToWaitGroup(fn.Body) {
					return
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if stmt, ok := n.(*ast.GoStmt); ok && !r.Exempt(f.Rel) {
						r.ReportAt(stmt.Pos(), "%s starts a goroutine without wg.Add; register it with startRunners", fn.Name.Name)
					}
					return true
				})
			})
		}
	},
}}

// compositeKind reports a slice, array or map type or literal.
func compositeKind(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.ArrayType:
		return "slice/array"
	case *ast.MapType:
		return "map"
	case *ast.CompositeLit:
		return compositeKind(t.Type)
	}
	return ""
}

func addsOneToWaitGroup(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && laws.SelectorName(call.Fun) == "wg.Add" && len(call.Args) == 1 {
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.INT && lit.Value == "1" {
				found = true
			}
		}
		return !found
	})
	return found
}
