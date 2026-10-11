package validation

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/boolexpr"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
)

// ValidateUserInteractionGates checks feedback/decision gate ids align with on_enter hooks.
func ValidateUserInteractionGates(m workflowdef.Manifest) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	feedbackPhases := map[string]bool{}
	decisionPhases := map[string]bool{}
	intakeKeys := map[string]string{}
	for _, p := range m.PhaseDefs {
		if fb := p.OnEnter.RequestUserFeedback; fb != nil {
			if fb.ResolvedResponseType().IsChoice() {
				decisionPhases[p.ID] = true
			} else {
				feedbackPhases[p.ID] = true
			}
		}
		if len(p.Intake) > 0 {
			for _, key := range p.Intake {
				key = strings.TrimSpace(key)
				if key != "" {
					intakeKeys[key] = p.ID
				}
			}
		}
	}
	for _, p := range m.PhaseDefs {
		idents := collectPhaseConditionIDs(p)
		for _, id := range idents {
			if phaseID, ok := userFeedbackReceivedPhase(id); ok {
				field := fmt.Sprintf("phases[%s].complete_when", p.ID)
				if !feedbackPhases[phaseID] {
					out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("feedback_gate_phase_missing"), field,
						map[string]any{"phase": phaseID, "leaf": id}))
				}
				if feedbackPhases[p.ID] && phaseID != p.ID {
					out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("feedback_gate_phase_mismatch"), field,
						map[string]any{"phase": p.ID, "leaf": id}))
				}
			}
			if key, ok := userDecisionGatePhase(id); ok {
				field := fmt.Sprintf("phases[%s].complete_when", p.ID)
				if phaseID, isIntake := intakeKeys[key]; isIntake {
					if p.ID != phaseID {
						out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("intake_gate_phase_mismatch"), field,
							map[string]any{"phase": phaseID, "leaf": id}))
					}
					continue
				}
				if !decisionPhases[key] {
					out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("decision_gate_phase_missing"), field,
						map[string]any{"phase": key, "leaf": id}))
				}
				if decisionPhases[p.ID] && key != p.ID {
					out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("decision_gate_phase_mismatch"), field,
						map[string]any{"phase": p.ID, "leaf": id}))
				}
			}
		}
	}
	return out
}

func collectPhaseConditionIDs(p workflowdef.PhaseDef) []string {
	var exprs []string
	if cw := strings.TrimSpace(p.CompleteWhen); cw != "" && cw != workflowdef.CompleteWhenGatesSatisfied {
		exprs = append(exprs, cw)
	}
	if ew := strings.TrimSpace(p.EntryWhen); ew != "" {
		exprs = append(exprs, ew)
	}
	for _, g := range p.Gates {
		if g = strings.TrimSpace(g); g != "" {
			exprs = append(exprs, g)
		}
	}
	return collectConditionIDs(exprs...)
}

func collectConditionIDs(exprs ...string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, expr := range exprs {
		expr = strings.TrimSpace(expr)
		if expr == "" {
			continue
		}
		if !NeedsExpressionParser(expr) {
			seen[expr] = struct{}{}
			continue
		}
		node, err := boolexpr.Parse(expr)
		if err != nil {
			continue
		}
		for _, id := range boolexpr.CollectIdents(node) {
			seen[id] = struct{}{}
		}
	}
	for id := range seen {
		out = append(out, id)
	}
	return out
}

func userFeedbackReceivedPhase(ident string) (string, bool) {
	const prefix = "user_feedback_received:"
	if !strings.HasPrefix(ident, prefix) {
		return "", false
	}
	id := strings.TrimSpace(strings.TrimPrefix(ident, prefix))
	return id, id != ""
}

func userDecisionGatePhase(ident string) (string, bool) {
	switch {
	case strings.HasPrefix(ident, "user_decision_received:"):
		id := strings.TrimSpace(strings.TrimPrefix(ident, "user_decision_received:"))
		return id, id != ""
	case strings.HasPrefix(ident, "user_decision:"):
		rest := strings.TrimSpace(strings.TrimPrefix(ident, "user_decision:"))
		id, _, _ := strings.Cut(rest, ",")
		id = strings.TrimSpace(id)
		return id, id != ""
	default:
		return "", false
	}
}
