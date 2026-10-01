package workflow

import (
	"fmt"
	"sort"
	"strings"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
)

// ValidatePhaseObligations checks declarations, gates, and host advancement.
func ValidatePhaseObligations(specs ObligationSpecs, m workflowdef.Manifest, p workflowdef.PhaseDef) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	gated := map[string]struct{}{}
	for _, g := range p.Gates {
		if kind, ok := ObligationKindFromGateLeaf(g); ok {
			gated[kind] = struct{}{}
		}
	}
	declared := map[string]struct{}{}
	for _, ob := range p.OnEnter.Obligations {
		declared[ob.Kind] = struct{}{}
		spec, registered := specs[ob.Kind]
		if !registered {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("obligation_kind_unknown"),
				fmt.Sprintf("phases[%s].on_enter.obligations", p.ID),
				map[string]any{"phase": p.ID, "kind": ob.Kind, "kinds": strings.Join(specKinds(specs), ", ")}))
		} else if err := spec.ValidateParams(ob.Params); err != nil {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("obligation_params_invalid"),
				fmt.Sprintf("phases[%s].on_enter.obligations", p.ID),
				map[string]any{"phase": p.ID, "kind": ob.Kind, "detail": err.Error()}))
		}
		if _, ok := gated[ob.Kind]; !ok {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("obligation_gate_missing"),
				fmt.Sprintf("phases[%s].gates", p.ID),
				map[string]any{"phase": p.ID, "kind": ob.Kind}))
		}
	}
	for kind := range gated {
		if _, ok := declared[kind]; !ok {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("obligation_not_declared"),
				fmt.Sprintf("phases[%s].on_enter.obligations", p.ID),
				map[string]any{"phase": p.ID, "kind": kind}))
		}
	}
	if p.HasOnEnterObligations() && workflowdef.EffectiveAdvancePolicy(m, p) != workflowdef.AdvanceWhenGateMetAuto {
		out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("obligation_advance_not_host"),
			fmt.Sprintf("phases[%s].advance", p.ID),
			map[string]any{"phase": p.ID}))
	}
	out = append(out, ValidateHostObligationWake(m, p)...)
	return out
}

// ValidateHostObligationWake rejects a held phase whose settle lands on a phase
// that never prompts: the park has no timer, so the settle is the only wake. A
// target with its own obligations chains the wait and is checked on its own row.
func ValidateHostObligationWake(m workflowdef.Manifest, p workflowdef.PhaseDef) []api.ComposeValidationError {
	if !p.HasOnEnterObligations() || p.Terminal {
		return nil
	}
	target := strings.TrimSpace(p.Next)
	if target == "" {
		// No declared successor: the run completes, which needs no wake.
		return nil
	}
	def, ok := m.PhaseByID(target)
	if !ok {
		// An unresolved next is reported by topology validation.
		return nil
	}
	if def.Terminal || def.OnEnter.PromptCoordinator || def.HasOnEnterObligations() {
		return nil
	}
	return []api.ComposeValidationError{workflowdiag.EmitDefault(
		workflowdiag.MustCode("obligation_wake_unreachable"),
		fmt.Sprintf("phases[%s].on_enter", target),
		map[string]any{"phase": p.ID, "target": target},
	)}
}

func specKinds(specs ObligationSpecs) []string {
	out := make([]string, 0, len(specs))
	for kind := range specs {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}
