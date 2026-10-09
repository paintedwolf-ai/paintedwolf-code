package validation

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/boolexpr"
	"github.com/lycaon/lycaon/internal/conditions"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"github.com/lycaon/lycaon/internal/theme"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
)

// ObligationSpec validates one obligation kind.
type ObligationSpec interface {
	Kind() string
	ValidateParams(params map[string]any) error
}

// ObligationSpecs maps kind name to its param validator.
type ObligationSpecs map[string]ObligationSpec

// ValidateComposeManifest returns every manifest validation problem.
func ValidateComposeManifest(reg *conditions.ConditionRegistry, specs ObligationSpecs, m workflowdef.Manifest) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	for _, p := range m.PhaseDefs {
		cw := strings.TrimSpace(p.CompleteWhen)
		if cw == "" || cw == workflowdef.CompleteWhenGatesSatisfied {
			// gates[] validated below
		} else if strings.HasPrefix(cw, workflowdef.CompleteWhenGateSatisfied) {
			gate := strings.TrimPrefix(cw, workflowdef.CompleteWhenGateSatisfied)
			out = append(out, validateComposeLeafDiag(reg, fmt.Sprintf("phases[%s].complete_when", p.ID), gate)...)
		} else {
			out = append(out, validateComposeExprLeavesDiag(reg, fmt.Sprintf("phases[%s].complete_when", p.ID), cw)...)
		}
		for i, g := range p.Gates {
			g = strings.TrimSpace(g)
			if g == "" {
				continue
			}
			out = append(out, validateComposeLeafDiag(reg, fmt.Sprintf("phases[%s].gates[%d]", p.ID, i), g)...)
		}
		if ew := strings.TrimSpace(p.EntryWhen); ew != "" {
			out = append(out, validateComposeExprLeavesDiag(reg, fmt.Sprintf("phases[%s].entry_when", p.ID), ew)...)
		}
		if fb := p.OnEnter.RequestUserFeedback; fb != nil {
			if strings.TrimSpace(fb.Prompt) == "" {
				out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("prompt_required"),
					fmt.Sprintf("phases[%s].on_enter.request_user_feedback.prompt", p.ID),
					map[string]any{"phase": p.ID}))
			}
			if fb.ResolvedResponseType().IsChoice() && len(fb.Options) < 2 {
				out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("feedback_options_required"),
					fmt.Sprintf("phases[%s].on_enter.request_user_feedback.options", p.ID),
					map[string]any{"phase": p.ID}))
			}
		}
		if sp := strings.TrimSpace(p.OnEnter.SetPosture); sp != "" {
			if !sessionposture.ValidSessionPosture(sp) {
				out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("invalid_posture"),
					fmt.Sprintf("phases[%s].on_enter.set_posture", p.ID),
					map[string]any{"posture": sp}))
			}
		}
		if sem := strings.TrimSpace(p.OnEnter.SetExecutionMode); sem != "" {
			if err := workflowdef.ValidateExecutionModeField(fmt.Sprintf("phases[%s].on_enter.set_execution_mode", p.ID), sem); err != nil {
				out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("invalid_execution_mode"),
					fmt.Sprintf("phases[%s].on_enter.set_execution_mode", p.ID),
					map[string]any{"mode": sem}))
			}
		}
	}
	out = append(out, ValidateUserInteractionGates(m)...)
	out = append(out, ValidatePrimitiveManifest(m)...)
	out = append(out, ValidateReportControlHasReportPhase(m)...)
	out = append(out, ValidateBlueprintWritePhases(m)...)
	for _, p := range m.PhaseDefs {
		out = append(out, ValidateTerminalStamp(p)...)
		out = append(out, ValidateRequiredCoordinatorSurface(m, p)...)
		out = append(out, ValidateReportPhaseSurfaceExit(m, p)...)
		out = append(out, ValidateReviewAgentsDeclared(m, p)...)
		out = append(out, ValidatePhaseObligations(specs, m, p)...)
	}
	if ip := strings.TrimSpace(m.InitialPosture); ip != "" && !sessionposture.ValidSessionPosture(ip) {
		out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("invalid_posture"), "initial_posture",
			map[string]any{"posture": ip}))
	}
	// An unknown `icon:` still renders as the generic workflow mark, so this
	// reports a choice that was not honored rather than a broken pack.
	if icon := strings.TrimSpace(m.Icon); icon != "" {
		if _, ok := theme.IconSlotByID(icon); !ok {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("invalid_icon"), "icon",
				map[string]any{"icon": icon}))
		}
	}
	if dem := strings.TrimSpace(m.Controls.DefaultExecutionMode); dem != "" {
		if err := workflowdef.ValidateExecutionModeField("controls.default_execution_mode", dem); err != nil {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("invalid_execution_mode"),
				"controls.default_execution_mode", map[string]any{"mode": dem}))
		}
	}
	return out
}

func validateComposeLeafDiag(reg *conditions.ConditionRegistry, field, id string) []api.ComposeValidationError {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if err := conditions.ForbiddenPredicateConfigError(id); err != nil {
		hint := conditions.ForbiddenPredicateReplacementHint(id)
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("forbidden_predicate"), field,
			map[string]any{"leaf": id, "replacement_hint": hint})}
	}
	if reg == nil || (!reg.Has(id) && !workflowdef.IsKnownGateLeaf(id)) {
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("unknown_predicate"), field,
			map[string]any{"leaf": id})}
	}
	return nil
}

func validateComposeExprLeavesDiag(reg *conditions.ConditionRegistry, field, expr string) []api.ComposeValidationError {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil
	}
	if !NeedsExpressionParser(expr) {
		return validateComposeLeafDiag(reg, field, expr)
	}
	node, err := boolexpr.Parse(expr)
	if err != nil {
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("boolexpr_parse"), field,
			map[string]any{"detail": err.Error()})}
	}
	var out []api.ComposeValidationError
	for _, id := range boolexpr.CollectIdents(node) {
		out = append(out, validateComposeLeafDiag(reg, field, id)...)
	}
	return out
}

func NeedsExpressionParser(expr string) bool {
	lower := strings.ToLower(expr)
	return strings.Contains(lower, " and ") ||
		strings.Contains(lower, " or ") ||
		strings.Contains(lower, " not ") ||
		strings.Contains(expr, "(")
}
