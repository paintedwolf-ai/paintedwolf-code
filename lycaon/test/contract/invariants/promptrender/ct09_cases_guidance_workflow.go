package promptrender

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/casegen"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
)

// activeWorkflowCases renders inject.RenderActiveWorkflowInject on stock
// coordinators of active runs over the runtime facts the frame carries: the
// current phase's gate states and the leave recipe derived from them, its
// completion condition, an inherited Blueprint approval
// (workflow.Approvals.InheritedApprovalSnapshot yields approved or invalid)
// and a compose summary requiring isolation. Gate obligations are left out:
// their feedback is rendered and checked per leaf elsewhere.
func activeWorkflowCases(_ context.Context, e *guidanceCaseEnv) ([]guidanceCase, error) {
	reps := coordinatorRepsBy(e.cReps,
		func(cr coordRep) bool { return cr.state.TurnProfile.WorkflowActive },
		func(cr coordRep) string { return offers(cr.auth, "promote_overlay", "ask_user", "workflow_advance") })
	if len(reps) == 0 {
		return nil, fmt.Errorf("no stock coordinator on an active run")
	}
	gateStates := []string{"open", "failed", "satisfied", "dormant", "none", "open-two", "dormant-two"}
	knobs := []casegen.Knob{
		repKnob("rep", []string{"profile_has_promote_overlay", "profile_has_ask_user", "profile_has_workflow_advance", casegen.AnyFact}, len(reps), func(i int) string { return reps[i].state.ID }),
		indexKnob("gates", []string{"unsatisfied_gate_leaves", "failed_leaves", "current_phase_gates", "phase_exit.open_gates", "phase_exit.dormant_gates"}, gateStates),
		indexKnob("complete_when", []string{"complete_when", "phase_exit.complete_when"}, []string{"none", "set"}),
		indexKnob("blueprint", []string{"blueprint_approval", "blueprint_approval.status == \"approved\""}, []string{"none", "approved", "invalid"}),
		indexKnob("isolation", []string{"requires_isolation"}, []string{"no", "yes"}),
	}
	return []guidanceCase{{
		unit: "guidance/active-workflow", recipient: interactions.RecipientCoordinator, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			cr := reps[knobValue[int](knobs, set, "rep")]
			row := inject.WorkflowPhaseRow{ID: "draft", Next: "review"}
			exit := &inject.PhaseExitView{Kind: "gates"}
			runCtx := api.CoordinatorRunContext{
				WorkflowID: "plan", WorkflowVersion: "1.0.0", RunID: "run-ct09", RunStatus: string(api.WorkflowRunStatusRunning),
				CurrentPhase: "draft", RequiresIsolation: knobValue[int](knobs, set, "isolation") == 1,
			}
			switch gateStates[knobValue[int](knobs, set, "gates")] {
			case "open":
				row.Gates = []inject.WorkflowGateState{{ID: "plan_stub_valid"}}
				exit.OpenGates = []string{"plan_stub_valid"}
			case "failed":
				row.Gates = []inject.WorkflowGateState{{ID: "plan_stub_valid"}}
				exit.OpenGates = []string{"plan_stub_valid"}
				runCtx.FailedLeaves = []string{"plan_stub_valid"}
			case "satisfied":
				row.Gates = []inject.WorkflowGateState{{ID: "plan_stub_valid", Satisfied: true}}
			case "dormant":
				row.Gates = []inject.WorkflowGateState{{ID: "evidence_passed:plan_review", Dormant: true}}
				exit.DormantGates = []string{"evidence_passed:plan_review"}
			case "open-two":
				row.Gates = []inject.WorkflowGateState{{ID: "plan_stub_valid"}, {ID: "hitl_consulted:intake"}}
				exit.OpenGates = []string{"plan_stub_valid", "hitl_consulted:intake"}
			case "dormant-two":
				row.Gates = []inject.WorkflowGateState{{ID: "evidence_passed:plan_review", Dormant: true}, {ID: "evidence_passed:survey_claims", Dormant: true}}
				exit.DormantGates = []string{"evidence_passed:plan_review", "evidence_passed:survey_claims"}
			}
			if knobValue[int](knobs, set, "complete_when") == 1 {
				row.CompleteWhen, exit.CompleteWhen = "draft ready", "draft ready"
			}
			snap := inject.WorkflowRuntimeSnapshot{
				Phases:    []inject.WorkflowPhaseRow{row, {ID: "review", Terminal: true}},
				PhaseExit: exit,
			}
			switch knobValue[int](knobs, set, "blueprint") {
			case 1:
				snap.BlueprintApproval = &inject.BlueprintApprovalView{Status: "approved", Origin: "inherited", ParentRunID: "run-parent"}
			case 2:
				snap.BlueprintApproval = &inject.BlueprintApprovalView{Status: "invalid", Origin: "inherited", ParentRunID: "run-parent"}
			}
			block, err := inject.RenderActiveWorkflowInject(ctx, e.inj, cr.auth.Reader, "sess-wf-ct09", inject.CoordinatorTurnFrame{RunContext: runCtx, Runtime: snap}, nil, nil, nil)
			if err != nil {
				return guidanceRender{}, err
			}
			return coordinatorRender(block, cr), nil
		},
	}}, nil
}

// securityPhaseKickCases renders the security survey's phase-entry kicks on
// the stock recipients bound to their phases, over the review verdicts the
// run records per review-loop evidence key (runstate.StampReviewVerdict): none,
// either key alone, or both.
func securityPhaseKickCases(_ context.Context, e *guidanceCaseEnv) ([]guidanceCase, error) {
	wfReg, err := workflowdef.RegistryFromDirs("")
	if err != nil {
		return nil, err
	}
	anchorReg, err := e.anchorRegistry()
	if err != nil {
		return nil, err
	}
	pairs, err := cachedCoordinatorPairs()
	if err != nil {
		return nil, err
	}
	byBinding := boundRecipients(pairs)
	kicks := []string{"coordinator-security-challenge", "coordinator-security-synthesis"}
	recipients := map[string][]coordRep{}
	for _, key := range sortedKeys(wfReg.All()) {
		m := wfReg.All()[key]
		for _, p := range m.PhaseDefs {
			b, err := hoststate.BindPhase(m, p)
			if err != nil {
				return nil, err
			}
			for _, ev := range phaseEvents(m, p, b, anchorReg) {
				if slices.Contains(kicks, ev.render) {
					recipients[ev.render] = append(recipients[ev.render], byBinding[b.Key()]...)
				}
			}
		}
	}
	verdicts := []string{"both", "claims", "challenged", "none"}
	claims := map[string]any{"threat_model": "Untrusted HTTP input", "claims": "- claim-1: validated"}
	challenged := map[string]any{"challenges": "- claim-1: survives"}
	var out []guidanceCase
	for _, kick := range kicks {
		reps := recipients[kick]
		if len(reps) == 0 {
			continue
		}
		knobs := []casegen.Knob{
			repKnob("rep", []string{casegen.AnyFact}, len(reps), func(i int) string { return reps[i].state.ID }),
			indexKnob("verdicts", []string{"review_verdict", "review_verdict.survey_claims", "review_verdict.survey_challenged"}, verdicts),
		}
		out = append(out, guidanceCase{
			unit: "guidance/" + kick, recipient: interactions.RecipientCoordinator, knobs: knobs,
			render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
				cr := reps[knobValue[int](knobs, set, "rep")]
				data := maps.Clone(phaseKickVariants()[0])
				delete(data, "review_verdict")
				switch verdicts[knobValue[int](knobs, set, "verdicts")] {
				case "both":
					data["review_verdict"] = map[string]any{"survey_claims": claims, "survey_challenged": challenged}
				case "claims":
					data["review_verdict"] = map[string]any{"survey_claims": claims}
				case "challenged":
					data["review_verdict"] = map[string]any{"survey_challenged": challenged}
				}
				if err := prompts.MergeCoordinatorKickVarsForTools(data, cr.auth.Reader); err != nil {
					return guidanceRender{}, err
				}
				text, err := e.s.Engine().RenderKick(ctx, cr.auth.Reader, kick, data)
				if err != nil {
					return guidanceRender{}, err
				}
				return coordinatorRender(text, cr), nil
			},
		})
	}
	return out, nil
}
