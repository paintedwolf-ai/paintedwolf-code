package forbiddencalls

import (
	"go/ast"
	"path"
	"regexp"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

// contentSniffArg names a variable that holds tool output.
var contentSniffArg = regexp.MustCompile(`(?i)(content|output|payload|result|body|text|trimmed|line|jsonPart|banners)`)

var toolRoutingFiles = []string{
	"internal/llm/compaction/chunk_compactor.go",
	"internal/llm/compaction/overlay_promote_chunk.go",
	"internal/llm/compaction/tool_wire_compact.go",
}

// visualProducers must each assign Out.Visual; new producers need no entry.
var visualProducers = []string{
	"internal/tools/native/page/measure.go",
	"internal/tools/native/page/session.go",
	"internal/tools/native/page/render_view.go",
	"internal/tools/native/terminal/session_snapshot.go",
	"internal/visual/test_producer.go",
}

var toolOutput = []invariants.Invariant{{
	// JSON that can reach model context keeps HTML-unsafe characters literal,
	// through surveyjson.Marshal.
	Name:    "survey-tools-use-surveyjson-marshal",
	Anchors: []invariants.Anchor{invariants.Func("internal/tools/surveyjson", "Marshal")},
	Exempt:  surveyJSONExempt,
	Check: func(r *invariants.Run) {
		dirs := []string{"internal/tools/native", "internal/scan", "internal/repomap", "internal/git", "internal/webresearch",
			"internal/coordinator/loopwake", "internal/tooloutput", "internal/browser"}
		for _, f := range r.GoFiles(invariants.Production, dirs...) {
			if invariants.Under(f, "internal/tools/surveyjson") {
				continue
			}
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if pkg == "encoding/json" && (name == "Marshal" || name == "MarshalIndent" || name == "NewEncoder") {
					if !r.Exempt(f.Rel) {
						r.ReportAt(sel.Pos(), "json.%s on a path to model context; use surveyjson.Marshal", name)
					}
				}
			})
		}
	},
}, {
	// Tool results echo repository bytes, so compaction routes by tool
	// identity, never by sentinel strings the output may quote.
	Name:    "no-content-substring-tool-routing",
	Anchors: goPaths(toolRoutingFiles),
	Exempt: []invariants.Exemption{
		{Key: "overlay_promote_chunk.go:inspectOverlayPromoteToolContent", Reason: "shape check for the promote stamp; CompactChunk gates by diet stamp and classifier"},
	},
	Check: func(r *invariants.Run) {
		for _, rel := range toolRoutingFiles {
			f := r.GoFile(rel)
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				if pkg != "strings" || name != "Contains" || len(call.Args) != 2 {
					return
				}
				arg, ok := call.Args[0].(*ast.Ident)
				_, literal := invariants.StringLit(call.Args[1])
				if !ok || !literal || !contentSniffArg.MatchString(arg.Name) {
					return
				}
				site := path.Base(rel) + ":"
				if fn := f.EnclosingFunc(call.Pos()); fn != nil {
					site += fn.Name.Name
				}
				if !r.Exempt(site) {
					r.ReportAt(call.Pos(), "strings.Contains on %s selects by output content; route by tool identity", arg.Name)
				}
			})
		}
	},
}, {
	// Tool visuals attach in one place, promptloop, through AttachToolResult,
	// whose returned content the tool result carries.
	Name:    "tool-visuals-attach-only-in-promptloop",
	Anchors: append(goPaths(visualProducers), invariants.Func("internal/visual", "AttachToolResult")),
	Exempt:  []invariants.Exemption{{Key: "internal/coordinator/promptloop/tools.go", Reason: "the one tool-result path that attaches visuals"}},
	Check: func(r *invariants.Run) {
		producers := map[string]bool{}
		for _, f := range r.GoFiles(invariants.Production, "internal") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.AssignStmt:
					for i, lhs := range node.Lhs {
						if i < len(node.Rhs) && isOutVisualSelector(lhs) && assignsVisualCapture(node.Rhs[i]) {
							producers[f.Rel] = true
						}
					}
					for _, rhs := range node.Rhs {
						if isAttachToolResultCall(rhs) && !r.Exempt(f.Rel) {
							r.ReportAt(rhs.Pos(), "attaches a tool visual outside promptloop")
						}
						if id, ok := node.Lhs[0].(*ast.Ident); ok && id.Name == "_" && isAttachToolResultCall(rhs) {
							r.ReportAt(rhs.Pos(), "discards AttachToolResult's stamped content")
						}
					}
				case *ast.ExprStmt:
					if isAttachToolResultCall(node.X) {
						r.ReportAt(node.Pos(), "discards AttachToolResult's stamped content")
					}
				case *ast.CompositeLit:
					if !invariants.Under(f, "internal/visual") && invariants.TypeName(node.Type) == "VisualArtifact" && setsField(node, "ToolCallID") {
						r.ReportAt(node.Pos(), "VisualArtifact{ToolCallID: ...} bypasses the AttachToolResult stamp")
					}
				}
				return true
			})
		}
		for _, rel := range visualProducers {
			if !producers[rel] {
				r.Report("%s no longer produces a VisualCapture", rel)
			}
		}
	},
}}

func isOutVisualSelector(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Visual" {
		return false
	}
	// tctx.Out.Visual or Out.Visual
	switch x := sel.X.(type) {
	case *ast.Ident:
		return x.Name == "Out"
	case *ast.SelectorExpr:
		return x.Sel != nil && x.Sel.Name == "Out"
	default:
		return false
	}
}

func assignsVisualCapture(expr ast.Expr) bool {
	expr = stripUnary(expr)
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return false
	}
	switch typ := lit.Type.(type) {
	case *ast.Ident:
		return typ.Name == "VisualCapture"
	case *ast.SelectorExpr:
		return typ.Sel != nil && typ.Sel.Name == "VisualCapture"
	default:
		return false
	}
}

func stripUnary(expr ast.Expr) ast.Expr {
	for {
		u, ok := expr.(*ast.UnaryExpr)
		if !ok {
			return expr
		}
		expr = u.X
	}
}

func isAttachToolResultCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fun.Sel != nil && fun.Sel.Name == "AttachToolResult"
	case *ast.Ident:
		return fun.Name == "AttachToolResult"
	default:
		return false
	}
}

// surveyJSONExempt emit JSON that never reaches model context.
var surveyJSONExempt = []invariants.Exemption{
	{Key: "internal/scan/output/ledger_export.go", Reason: "HTTP SARIF/OpenVEX attachment exports; not model tool output"},
	{Key: "internal/scan/bundled/build_identity.go", Reason: "packaged build identity hashing"},
	{Key: "internal/scan/bundled/consumer_finalize.go", Reason: "consumer finalization record artifact"},
	{Key: "internal/scan/bundled/consumer_qualification.go", Reason: "consumer qualification report artifact"},
	{Key: "internal/scan/bundled/release_audit.go", Reason: "offline release audit artifact"},
	{Key: "internal/scan/bundled/release_evidence.go", Reason: "authenticated release evidence artifact"},
	{Key: "internal/scan/dedup.go", Reason: "internal dedup hash; payload never reaches LLM"},
	{Key: "internal/scan/findings/inventory_revision.go", Reason: "inventory revision hash; payload never reaches LLM"},
	{Key: "internal/scan/store_results.go", Reason: "SQL persistence; payload never reaches LLM directly"},
	{Key: "internal/scan/store_execution.go", Reason: "SQL persistence of runtime policy and progress; payload never reaches LLM directly"},
	{Key: "internal/scan/result_spill.go", Reason: "oversized result_json spill file + SQLite stub; never LLM wire"},
	{Key: "internal/browser/filmstrip.go", Reason: "zip manifest for Den asset pack; not tool_result JSON"},
}
