package forbiddencalls

import (
	"go/ast"

	"github.com/lycaon/lycaon/test/contract/laws"
)

const anchorPkg = "github.com/lycaon/lycaon/internal/coordinator/anchor"

var coordinator = []laws.Rule{{
	// A match context carries a workflow only through anchor.RunMatch, which
	// binds the workflow version.
	Name:    "match-context-workflow-through-run-match",
	Anchors: []laws.Anchor{laws.Decl("internal/coordinator/anchor", "MatchContext.Workflow"), laws.Func("internal/coordinator/anchor", "RunMatch")},
	Exempt:  []laws.Exemption{{Key: "internal/coordinator/anchor/binding.go:RunMatch", Reason: "the binding constructor itself"}},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isMatchContext(f, lit.Type) || !setsField(lit, "Workflow") || r.Exempt(f.Site(lit.Pos())) {
					return true
				}
				r.ReportAt(lit.Pos(), "builds MatchContext{Workflow: ...}; use anchor.RunMatch to bind the workflow version")
				return true
			})
		}
	},
}}

func isMatchContext(f *laws.GoFile, typ ast.Expr) bool {
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name == "MatchContext" && f.Pkg == anchorPkg
	}
	pkg, name, ok := f.Qualified(typ)
	return ok && pkg == anchorPkg && name == "MatchContext"
}

// setsField reports a keyed composite literal element for field.
func setsField(lit *ast.CompositeLit, field string) bool {
	for _, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == field {
				return true
			}
		}
	}
	return false
}

var transcriptPublish = []laws.Rule{{
	// AppendMessages stamps ord and seq onto its variadic slice; publishing
	// the pre-stamp value would pin the card at the top of the transcript.
	Name:    "publish-message-append-uses-the-stamped-copy",
	Anchors: []laws.Anchor{laws.Func("internal/events", "Publisher.PublishMessageAppend"), laws.Func("internal/session/store", "sqlTranscript.AppendMessages")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			f.Funcs(func(fn *ast.FuncDecl) {
				copied := map[string]bool{}
				f.Calls(fn.Body, func(call *ast.CallExpr, _, _ string) {
					switch laws.CalleeName(call) {
					case "AppendMessages":
						if len(call.Args) >= 3 && !call.Ellipsis.IsValid() {
							if id, ok := call.Args[len(call.Args)-1].(*ast.Ident); ok {
								copied[id.Name] = true
							}
						}
					case "PublishMessageAppend":
						if len(call.Args) < 4 {
							return
						}
						if id, ok := call.Args[len(call.Args)-1].(*ast.Ident); ok && copied[id.Name] {
							r.ReportAt(call.Pos(), "publishes %s after appending it by value; publish the stamped slice element", id.Name)
						}
					}
				})
			})
		}
	},
}}
