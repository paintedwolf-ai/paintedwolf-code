package promptrender

import (
	"context"
	"fmt"
	"maps"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

// phaseEvent is one workflow-event kick the host delivers on a bound phase.
type phaseEvent struct {
	render   string
	variants []map[string]any
}

// phaseKicks renders each workflow-event kick on the stock recipients bound
// to a phase the host delivers it on: the manifest's phase.entered injects,
// phase.exit_required where the phase's exit leaves advancing to the
// coordinator, and the review-loop kicks on review-loop phases. Further
// fixtures run on build posture.
func (k *kickSweep) phaseKicks(ctx context.Context, wfReg *workflowdef.Registry, anchorReg *anchor.Registry, reps []coordRep) error {
	byBinding := map[string][]coordRep{}
	for _, cr := range reps {
		if b := cr.state.PhaseBinding; b != nil && cr.state.PackOverlayID == "stock" {
			byBinding[b.Key()] = append(byBinding[b.Key()], cr)
		}
	}
	for _, wfKey := range sortedKeys(wfReg.All()) {
		m := wfReg.All()[wfKey]
		for _, p := range m.PhaseDefs {
			b, err := hoststate.BindPhase(m, p)
			if err != nil {
				return err
			}
			for _, ev := range phaseEvents(m, p, b, anchorReg) {
				for _, cr := range byBinding[b.Key()] {
					if err := k.phaseKick(ctx, wfKey, p.ID, ev, cr); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func phaseEvents(m workflowdef.Manifest, p workflowdef.PhaseDef, b hoststate.PhaseBinding, anchorReg *anchor.Registry) []phaseEvent {
	var out []phaseEvent
	for _, in := range m.Injects {
		if in.On == "phase.entered" && in.Selector.Phase != nil && *in.Selector.Phase == p.ID {
			out = append(out, phaseEvent{render: in.Render, variants: phaseKickVariants()})
		}
	}
	exit := workflowpresentation.ProjectPhaseExit(m, p, nil, nil).InjectView()
	if phaseExitRequiredOnPassedGates(m, p, b, exit) {
		out = append(out, phaseEvent{
			render:   anchorReg.InformRender(anchor.PhaseExitRequired),
			variants: []map[string]any{{"workflow_id": m.ID, "phase_id": p.ID}},
		})
	}
	if p.ReviewLoop != nil {
		for _, id := range []anchor.ID{anchor.ReviewLoopContinue, anchor.ReviewLoopDecide} {
			out = append(out, phaseEvent{render: anchorReg.InformRender(id), variants: []map[string]any{{}}})
		}
	}
	return out
}

// phaseExitRequiredOnPassedGates evaluates the host's exit-required decision
// for p once every gate it declares has passed.
func phaseExitRequiredOnPassedGates(m workflowdef.Manifest, p workflowdef.PhaseDef, b hoststate.PhaseBinding, exit *inject.PhaseExitView) bool {
	row := inject.WorkflowPhaseRow{ID: p.ID}
	for _, g := range p.Gates {
		row.Gates = append(row.Gates, inject.WorkflowGateState{ID: g, Satisfied: true})
	}
	return inject.WorkflowEvaluation(inject.CoordinatorTurnFrame{
		RunContext: api.CoordinatorRunContext{
			WorkflowID: m.ID, CurrentPhase: p.ID, RunStatus: string(api.WorkflowRunStatusRunning),
			AdvanceWhenGateMet: b.Advance,
		},
		Runtime: inject.WorkflowRuntimeSnapshot{Phases: []inject.WorkflowPhaseRow{row}, PhaseExit: exit},
	}).CoordinatorPhaseExitRequired()
}

func (k *kickSweep) phaseKick(ctx context.Context, wfKey, phaseID string, ev phaseEvent, cr coordRep) error {
	for vIdx, pv := range ev.variants {
		if vIdx > 0 && cr.state.Posture != api.SessionPostureBuild {
			continue
		}
		data := maps.Clone(pv)
		if err := prompts.MergeCoordinatorKickVarsForTools(data, cr.auth.Reader); err != nil {
			return err
		}
		text, rKey, err := k.render(ctx, "phasekick:", ev.render, cr.auth.Reader, data)
		if err != nil {
			return err
		}
		label := fmt.Sprintf("%s phase %s kick %s (%s)", wfKey, phaseID, ev.render, cr.state.ID)
		checkClaimsCached(k.cache, rKey, cr.auth.Digest, k.report, func() []string {
			return checkClaimsOnce(k.s, label, text, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, nil, true)
		})
	}
	return nil
}
