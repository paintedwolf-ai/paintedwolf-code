package forbiddencalls

import (
	"go/ast"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

// modeAwareEscalationReaders are the production files that consume
// IsEscalated on a mode-aware path (either gating directly on
// CircuitBreaker.EscalateMode == "block" or reading through
// AmbientGroundingCoordinator.IsEscalated, which enforces EscalateMode).
var modeAwareEscalationReaders = map[string]bool{
	"internal/coordinator/grounding/ambient.go":  true,
	"internal/coordinator/loopwake/admission.go": true,
	"internal/session/promptsource/loop.go":      true,
	"internal/session/turnexecution/run.go":      true,
	"internal/toolhost/grounding_service.go":     true,
}

var groundingEscalation = []invariants.Invariant{{
	// Reads of blocking escalation state (IsEscalated) are permitted only on
	// mode-aware paths, and AmbientGroundingCoordinator.IsEscalated itself
	// must guard on EscalateMode so advisory modes (warn/flag) never refuse.
	Name: "blocking-escalation-reads-are-mode-aware",
	Anchors: []invariants.Anchor{
		invariants.Func("internal/grounding", "StateStore.IsEscalated"),
		invariants.Func("internal/coordinator/grounding", "AmbientGroundingCoordinator.IsEscalated"),
	},
	Check: func(r *invariants.Run) {
		ambientFn := r.FuncDecl("internal/coordinator/grounding", "AmbientGroundingCoordinator.IsEscalated")
		checksMode := false
		ast.Inspect(ambientFn.Body, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "EscalateMode" {
				checksMode = true
				return false
			}
			return true
		})
		if !checksMode {
			r.ReportAt(ambientFn.Pos(), "AmbientGroundingCoordinator.IsEscalated does not check EscalateMode; advisory modes (warn/flag) must never refuse")
		}

		calls := 0
		for _, f := range r.GoFiles(invariants.Production, invariants.Source...) {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || invariants.CalleeName(call) != "IsEscalated" {
					return true
				}
				calls++
				if modeAwareEscalationReaders[f.Rel] || r.Exempt(f.Rel) {
					return true
				}
				r.ReportAt(call.Pos(), "IsEscalated called outside registered mode-aware paths; register a mode-aware guard in GB10 I5")
				return true
			})
		}
		r.Expect(calls, "calls to IsEscalated")
	},
}}
