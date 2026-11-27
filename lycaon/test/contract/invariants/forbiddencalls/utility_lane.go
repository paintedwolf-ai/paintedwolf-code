package forbiddencalls

import (
	"go/ast"
	"go/token"
	"slices"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

const (
	reviewDir   = "internal/workflow/review"
	outcomesDir = "internal/session/workeroutcomes"
	turnDir     = "internal/session/turnexecution"
	llmDir      = "internal/llm"
)

var utilityLane = []invariants.Invariant{{
	Name: "curation-stays-on-the-admitted-turn",
	Anchors: []invariants.Anchor{
		invariants.Func(reviewDir, "StampFanoutExecuteOutput"),
		invariants.Func(reviewDir, "ReviewEvidence.StampEvidence"),
		invariants.Func(reviewDir, "compileReviewLoopDigest"),
		invariants.Func(outcomesDir, "Results.EnvelopeForTerminal"),
		invariants.Func(turnDir, "Service.Run"),
		invariants.Func(llmDir, "RegistrySummarizer.completeUtility"),
		invariants.Func(llmDir, "RegistrySummarizer.streamLite"),
	},
	Check: func(r *invariants.Run) {
		for _, name := range []string{"StampFanoutExecuteOutput", "ReviewEvidence.StampEvidence", "compileReviewLoopDigest"} {
			forbidCurationSelectors(r, reviewDir, name, "MaybeCurateSynthesisEvidence", "Curate", "Complete")
		}
		forbidCurationSelectors(r, outcomesDir, "Results.EnvelopeForTerminal", "MaybeCurateSynthesisEvidence", "appendSynthesisEnvelope", "Curate")
		for _, f := range r.GoFiles(invariants.Production, "internal/workflow") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && (id.Name == "synthesisCurator" || id.Name == "SetSynthesisCurator") {
					r.ReportAt(id.Pos(), "workflow must not hold a synthesis curator")
				}
				return true
			})
		}
		checkAdmittedTurnOrder(r)
		for _, name := range []string{"RegistrySummarizer.completeUtility", "RegistrySummarizer.streamLite"} {
			decl := r.Declaration(llmDir, name)
			fn := decl.Node.(*ast.FuncDecl)
			calls := map[string]bool{}
			decl.File.Calls(fn.Body, func(call *ast.CallExpr, _, _ string) {
				calls[invariants.CalleeName(call)] = true
			})
			for _, required := range []string{"beginLaneCall", "endLaneCall"} {
				if !calls[required] {
					r.ReportAt(fn.Pos(), "%s must call %s", name, required)
				}
			}
		}
	},
}}

func forbidCurationSelectors(r *invariants.Run, dir, name string, forbidden ...string) {
	ast.Inspect(r.FuncDecl(dir, name).Body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && slices.Contains(forbidden, sel.Sel.Name) {
			r.ReportAt(sel.Pos(), "%s must not use %s outside the admitted turn", name, sel.Sel.Name)
		}
		return true
	})
}

func checkAdmittedTurnOrder(r *invariants.Run) {
	decl := r.Declaration(turnDir, "Service.Run")
	fn := decl.Node.(*ast.FuncDecl)
	steps := []string{"Begin", "WithSession", "WithLane", "executePromptRun"}
	positions := map[string]token.Pos{}
	decl.File.Calls(fn.Body, func(call *ast.CallExpr, _, _ string) {
		sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
		if !ok || !slices.Contains(steps, sel.Sel.Name) {
			return
		}
		name := sel.Sel.Name
		if name == "Begin" {
			owner, ok := sel.X.(*ast.SelectorExpr)
			if !ok || owner.Sel.Name != "Turns" {
				return
			}
		}
		if len(call.Args) == 0 || !identNamed(call.Args[0], "ctx") {
			r.ReportAt(call.Pos(), "%s must receive the admitted turn context", name)
		}
		if positions[name].IsValid() {
			r.ReportAt(call.Pos(), "duplicate %s in admitted turn sequence", name)
		}
		positions[name] = call.Pos()
	})
	prior := token.NoPos
	for _, name := range steps {
		pos := positions[name]
		if !pos.IsValid() || pos <= prior {
			r.ReportAt(fn.Pos(), "turn admission/session/lane/execution order broken at %s", name)
		}
		prior = pos
	}
}

func identNamed(expr ast.Expr, name string) bool {
	id, ok := ast.Unparen(expr).(*ast.Ident)
	return ok && id.Name == name
}
