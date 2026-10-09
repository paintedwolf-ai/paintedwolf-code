package validation

import (
	"fmt"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowintake "github.com/lycaon/lycaon/internal/workflow/intake"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// ValidatePrimitiveManifest checks intake, depth, review_loop, and human_approval invariants.
func ValidatePrimitiveManifest(m workflowdef.Manifest) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	intakePhases := map[string]string{}
	for _, p := range m.PhaseDefs {
		if len(p.Intake) > 0 {
			catalog, err := workflowintake.Bundled()
			if err != nil {
				out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("intake_catalog_error"),
					fmt.Sprintf("phases[%s].intake", p.ID),
					map[string]any{"phase": p.ID, "detail": err.Error()}))
				continue
			}
			for _, key := range p.Intake {
				key = strings.TrimSpace(key)
				if key == "" {
					continue
				}
				intakePhases[key] = p.ID
				if _, ok := catalog.Get(key); !ok {
					out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("intake_key_unknown"),
						fmt.Sprintf("phases[%s].intake", p.ID),
						map[string]any{"phase": p.ID, "key": key}))
				}
			}
		}
		if p.DepthParam != "" {
			spec, ok := m.Parameters[p.DepthParam]
			if !ok || spec.Type != "depth" {
				out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("depth_param_missing"),
					fmt.Sprintf("phases[%s].depth_param", p.ID),
					map[string]any{"phase": p.ID, "param": p.DepthParam}))
			}
		}
		if p.ReviewLoop != nil {
			key := strings.TrimSpace(p.ReviewLoop.EvidenceKey)
			if !reviewLoopGateReferenced(m, p.ID, key) {
				out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("review_loop_evidence_unreferenced"),
					fmt.Sprintf("phases[%s].review_loop", p.ID),
					map[string]any{"phase": p.ID, "key": key}))
			}
		}
		if p.HumanApproval != nil && m.Blueprint == nil {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("human_approval_blueprint_required"),
				fmt.Sprintf("phases[%s].human_approval", p.ID),
				map[string]any{"phase": p.ID}))
		}
	}
	out = append(out, validateIntakeDecisionGates(m, intakePhases)...)
	return out
}

func reviewLoopGateReferenced(m workflowdef.Manifest, phaseID, evidenceKey string) bool {
	evidenceKey = strings.TrimSpace(evidenceKey)
	if evidenceKey == "" {
		return false
	}
	want := "evidence_passed:" + evidenceKey
	for _, p := range m.PhaseDefs {
		if p.ID != phaseID {
			continue
		}
		for _, g := range p.Gates {
			if strings.TrimSpace(g) == want {
				return true
			}
		}
		if strings.TrimSpace(p.CompleteWhen) == want {
			return true
		}
	}
	return false
}

func validateIntakeDecisionGates(m workflowdef.Manifest, intakePhases map[string]string) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	for _, p := range m.PhaseDefs {
		idents := collectPhaseConditionIDs(p)
		for _, id := range idents {
			if phaseID, ok := userDecisionGatePhase(id); ok {
				if intakePhase, ok := intakePhases[phaseID]; ok {
					if p.ID != intakePhase {
						out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("intake_gate_phase_mismatch"),
							fmt.Sprintf("phases[%s].gates", p.ID),
							map[string]any{"phase": intakePhase, "leaf": id}))
					}
				}
			}
		}
	}
	for key, phaseID := range intakePhases {
		gate := "user_decision_received:" + key
		found := false
		for _, p := range m.PhaseDefs {
			if p.ID != phaseID {
				continue
			}
			for _, g := range p.Gates {
				if strings.TrimSpace(g) == gate {
					found = true
					break
				}
			}
		}
		if !found {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("intake_gate_missing"),
				fmt.Sprintf("phases[%s].intake", phaseID),
				map[string]any{"phase": phaseID, "key": key}))
		}
	}
	return out
}
