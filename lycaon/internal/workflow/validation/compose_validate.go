package validation

import (
	"fmt"
	"strings"

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
		out = append(out, validatePhasePredicates(reg, fmt.Sprintf("phases[%s]", p.ID), p)...)
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
