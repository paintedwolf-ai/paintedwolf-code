package forbiddencalls

import (
	"go/ast"
	"go/token"
	"strings"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

// goroutineShutdownPackages hold the serving goroutines that shutdown joins.
var goroutineShutdownPackages = []string{"internal/app", "internal/session", "internal/app/security", "internal/session/turnadmission"}

var globals = []invariants.Invariant{{
	// Shipped code exports no slice or map global every importer could
	// mutate; composites are exposed through copy or lookup functions.
	Name:    "no-exported-mutable-slice-or-map-globals",
	Anchors: []invariants.Anchor{invariants.Package("internal/app")},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal", "cmd", "pkg", "config") {
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
	Anchors: []invariants.Anchor{invariants.Package("internal/app"), invariants.Package("internal/session/turnadmission")},
	Exempt: []invariants.Exemption{
		{Key: "internal/app/runners.go", Reason: "startRunners pairs wg.Add with runner goroutines"},
		{Key: "internal/app/serve.go", Reason: "Run uses http.Server.Shutdown for the listen goroutine"},
		{Key: "internal/app/parent_watch.go", Reason: "Run cancels the parent watch and joins its channel before draining"},
		{Key: "internal/app/security/evidence.go", Reason: "the secret-harvest sweep is an event callback the bound tree sweep coalesces and finishes"},
		{Key: "internal/session/turnadmission/queue_round_drain.go", Reason: "round-end drains register with roundEndDrains, which Service.Wait cancels and joins"},
	},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, goroutineShutdownPackages...) {
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
		if call, ok := n.(*ast.CallExpr); ok && invariants.SelectorName(call.Fun) == "wg.Add" && len(call.Args) == 1 {
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.INT && lit.Value == "1" {
				found = true
			}
		}
		return !found
	})
	return found
}

// retentionIdentityKeys name runtime identity domains.
var retentionIdentityKeys = []string{"session", "Session", "job", "Job", "message", "Message", "chat", "Chat"}

var retention = []invariants.Invariant{{
	// Every store keyed by runtime identity is bounded by type, released on
	// disposal, or justified as process lifetime.
	Name:    "retention-closed-set-is-declared",
	Anchors: []invariants.Anchor{invariants.Package("internal/scopedstore")},
	Exempt:  retentionAnswers,
	Check: func(r *invariants.Run) {
		dirs := []string{"internal/session", "internal/llm", "internal/coordinator/assembly", "internal/tools/native",
			"internal/settings", "internal/secretharvest", "internal/grantedpath"}
		for _, f := range r.GoFiles(invariants.Production, dirs...) {
			check := func(name string, typ ast.Expr, pos token.Pos) {
				if !retentionStoreType(typ) || !retentionIdentityKeyed(name, typ) && !r.Exempt(f.Rel+"#"+name) {
					return
				}
				if sel, ok := unwrapSelector(typ); ok && sel == "scopedstore.LRU" || r.Exempt(f.Rel+"#"+name) {
					return
				}
				r.ReportAt(pos, "%s#%s is keyed by runtime identity and declares no retention bound", f.Rel, name)
			}
			ast.Inspect(f.AST, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.StructType:
					for _, field := range node.Fields.List {
						for _, name := range field.Names {
							check(name.Name, field.Type, name.Pos())
						}
					}
				case *ast.GenDecl:
					if node.Tok != token.VAR {
						return true
					}
					for _, spec := range node.Specs {
						if vs := spec.(*ast.ValueSpec); vs.Type != nil {
							for _, name := range vs.Names {
								check(name.Name, vs.Type, name.Pos())
							}
						}
					}
				}
				return true
			})
		}
	},
}}

func retentionIdentityKeyed(name string, expr ast.Expr) bool {
	if sel, ok := unwrapSelector(expr); ok && sel == "scopedstore.LRU" {
		return true
	}
	for _, key := range retentionIdentityKeys {
		if strings.Contains(name, key) {
			return true
		}
	}
	return false
}

// retentionStoreType reports supported keyed store types.
func retentionStoreType(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.MapType:
		return true
	case *ast.StarExpr:
		return retentionStoreType(t.X)
	case *ast.SelectorExpr:
		pkg, ok := t.X.(*ast.Ident)
		if !ok {
			return false
		}
		return (pkg.Name == "sync" && t.Sel.Name == "Map") ||
			(pkg.Name == "scopedstore" && (t.Sel.Name == "LRU" || t.Sel.Name == "Map"))
	case *ast.IndexExpr:
		return retentionStoreType(t.X)
	}
	return false
}

func unwrapSelector(expr ast.Expr) (string, bool) {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return unwrapSelector(t.X)
	case *ast.IndexExpr:
		return unwrapSelector(t.X)
	case *ast.SelectorExpr:
		if pkg, ok := t.X.(*ast.Ident); ok {
			return pkg.Name + "." + t.Sel.Name, true
		}
	}
	return "", false
}

// retentionAnswers declare stores keyed by runtime identity whose bound is
// release on disposal or a justified process lifetime; see docs/session.md.
var retentionAnswers = []invariants.Exemption{
	// Session disposal closes admission, drains jobs and drops the index.
	{Key: "internal/tools/native/command/program_supervision.go#sessions", Reason: "released when its identity is disposed"},
	// Each held job releases once; idle programs leave the supervisor index.
	{Key: "internal/tools/native/command/program_supervision.go#jobs", Reason: "released when its identity is disposed"},
	{Key: "internal/session/stream/hub.go#subs", Reason: "released when its identity is disposed"},
	{Key: "internal/session/stream/hub.go#sessionMsg", Reason: "released when its identity is disposed"},
	{Key: "internal/session/stream/hub.go#pending", Reason: "released when its identity is disposed"},
	{Key: "internal/session/stream/hub.go#timers", Reason: "released when its identity is disposed"},
	{Key: "internal/session/stream/active.go#bySession", Reason: "released when its identity is disposed"},
	{Key: "internal/session/stream/active.go#tokens", Reason: "released when its identity is disposed"},
	{Key: "internal/session/checkpoint/capture.go#bySession", Reason: "released when its identity is disposed"},
	{Key: "internal/session/turnsettlement/turn_lifecycle.go#bySession", Reason: "released when its identity is disposed"},
	{Key: "internal/session/promotionstate/promote_path_status.go#byJob", Reason: "released when its identity is disposed"},
	{Key: "internal/session/promotionstate/promote_path_status.go#previewJob", Reason: "released when its identity is disposed"},
	{Key: "internal/session/workerworkspace/touches.go#byJob", Reason: "released when its identity is disposed"},
	{Key: "internal/session/curation/service.go#bySession", Reason: "released when its identity is disposed"},
	{Key: "internal/session/turnadmission/queue_round_drain.go#bySession", Reason: "released when its identity is disposed"},
	{Key: "internal/secretharvest/secretharvest.go#bySession", Reason: "process lifetime, justified"},
	// Chat approvals: durable projections of chat_grants, released when the chat is disposed.
	{Key: "internal/session/approvalstate/sandbox_ask_guard.go#bySession", Reason: "released when its identity is disposed"},
	{Key: "internal/session/projectcontrol/service.go#lastSessionPosture", Reason: "process lifetime, justified"},
	{Key: "internal/session/projectcontrol/service.go#lastSessionExternalHidden", Reason: "process lifetime, justified"},
	{Key: "internal/settings/session_grants.go#bySession", Reason: "released when its identity is disposed"},
	{Key: "internal/settings/ask_quiet.go#byChat", Reason: "released when its identity is disposed"},
	{Key: "internal/grantedpath/grantedpath.go#byRoot", Reason: "released when its identity is disposed"},
}
