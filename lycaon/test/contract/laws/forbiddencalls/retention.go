package forbiddencalls

import (
	"go/ast"
	"go/token"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// retentionIdentityKeys name runtime identity domains.
var retentionIdentityKeys = []string{"session", "Session", "job", "Job", "message", "Message", "chat", "Chat"}

var retention = []laws.Rule{{
	// Every store keyed by runtime identity is bounded by type, released on
	// disposal, or justified as process lifetime.
	Name:    "retention-closed-set-is-declared",
	Anchors: []laws.Anchor{laws.Package("internal/scopedstore")},
	Exempt:  retentionAnswers,
	Check: func(r *laws.Run) {
		dirs := []string{"internal/session", "internal/llm", "internal/coordinator/assembly", "internal/tools/native",
			"internal/settings", "internal/secretharvest", "internal/grantedpath"}
		for _, f := range r.GoFiles(laws.Production, dirs...) {
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
var retentionAnswers = []laws.Exemption{
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
	{Key: "internal/session/store/memory.go#sessions", Reason: "process lifetime, justified"},
	{Key: "internal/session/store/memory.go#messages", Reason: "process lifetime, justified"},
	{Key: "internal/session/store/memory.go#untrustedRecords", Reason: "process lifetime, justified"},
	{Key: "internal/session/store/memory.go#secretExposureRecords", Reason: "process lifetime, justified"},
	{Key: "internal/secretharvest/secretharvest.go#bySession", Reason: "process lifetime, justified"},
	// Chat approvals: durable projections of chat_grants, released when the chat is disposed.
	{Key: "internal/session/approvalstate/sandbox_ask_guard.go#bySession", Reason: "released when its identity is disposed"},
	{Key: "internal/session/projectcontrol/service.go#lastSessionPosture", Reason: "process lifetime, justified"},
	{Key: "internal/session/projectcontrol/service.go#lastSessionExternalHidden", Reason: "process lifetime, justified"},
	{Key: "internal/settings/session_grants.go#bySession", Reason: "released when its identity is disposed"},
	{Key: "internal/settings/ask_quiet.go#byChat", Reason: "released when its identity is disposed"},
	{Key: "internal/grantedpath/grantedpath.go#byRoot", Reason: "released when its identity is disposed"},
}
