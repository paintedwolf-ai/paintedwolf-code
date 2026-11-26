package promptrender

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
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
// a compose summary requiring isolation, choice transitions, a stamped fan-out
// plan and the gate catalog's obligations for open leaves.
func activeWorkflowCases(_ context.Context, e *guidanceCaseEnv) ([]guidanceCase, error) {
	pairs, err := cachedCoordinatorPairs()
	if err != nil {
		return nil, err
	}
	reps := coordinatorRepsBy(pairs,
		func(cr coordRep) bool { return cr.state.TurnProfile.WorkflowActive },
		func(cr coordRep) string { return offers(cr.auth, "promote_overlay", "ask_user", "workflow_advance") })
	if len(reps) == 0 {
		return nil, fmt.Errorf("no stock coordinator on an active run")
	}
	gateStates := []string{"open", "failed", "satisfied", "dormant", "none", "open-two", "dormant-two", "failed-two", "failed-coordinator"}
	knobs := []casegen.Knob{
		repKnob([]string{"profile_has_promote_overlay", "profile_has_ask_user", "profile_has_workflow_advance", casegen.AnyFact}, len(reps), func(i int) string { return reps[i].state.ID }),
		indexKnob("gates", []string{"unsatisfied_gate_leaves", "failed_leaves", "current_phase_gates", "phase_exit.open_gates", "phase_exit.dormant_gates"}, gateStates),
		indexKnob("complete_when", []string{"complete_when", "phase_exit.complete_when"}, []string{"none", "set"}),
		indexKnob("blueprint", []string{"blueprint_approval", "blueprint_approval.status == \"approved\""}, []string{"none", "approved", "invalid"}),
		indexKnob("isolation", []string{"requires_isolation"}, []string{"no", "yes"}),
		indexKnob("request", []string{"request", "request.text", "request.source"}, []string{"none", "status", "text"}),
		indexKnob("roster", []string{"excluded_agents"}, []string{"none", "web-off", "web-off-coordinator"}),
		indexKnob("feedback", []string{"pending_feedback", "pending_feedback.prompt"}, []string{"none", "open", "prompt"}),
		indexKnob("report", []string{"report_document_enabled", "report_rating"}, []string{"none", "unrated", "rated"}),
		indexKnob("advance", []string{"phase_exit.coordinator_advances"}, []string{"host", "coordinator"}),
		indexKnob("choice", []string{"phase_exit.choice_transitions", "arm.coordinator_may_fire"}, []string{"none", "human", "coordinator"}),
		indexKnob("fanout", []string{"fanout_plan"}, []string{"none", "stamped"}),
		indexKnob("obligations", []string{"gate_obligations", "o.required", "o.missing"}, []string{"none", "attached"}),
	}
	gateCfg, err := feedback.LoadGateFeedbackCatalogWithCatalog(e.s.Catalog)
	if err != nil {
		return nil, err
	}
	resolvers := map[int]func(string) (toolvocabtest.ActorAuthority, error){}
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
			case "failed-coordinator":
				if !slices.Contains(cr.auth.Offered, "workflow_advance") {
					return guidanceRender{}, nil
				}
				row.Gates = []inject.WorkflowGateState{{ID: "plan_stub_valid"}}
				exit.OpenGates = []string{"plan_stub_valid"}
				runCtx.FailedLeaves = []string{"plan_stub_valid"}
				runCtx.AdvanceWhenGateMet, exit.CoordinatorAdvances = "coordinator", true
			case "failed-two":
				row.Gates = []inject.WorkflowGateState{{ID: "plan_stub_valid"}, {ID: "hitl_consulted:intake"}}
				exit.OpenGates = []string{"plan_stub_valid", "hitl_consulted:intake"}
				runCtx.FailedLeaves = []string{"plan_stub_valid", "hitl_consulted:intake"}
			case "dormant-two":
				row.Gates = []inject.WorkflowGateState{{ID: "evidence_passed:plan_review", Dormant: true}, {ID: "evidence_passed:survey_claims", Dormant: true}}
				exit.DormantGates = []string{"evidence_passed:plan_review", "evidence_passed:survey_claims"}
			}
			if knobValue[int](knobs, set, "advance") == 1 {
				// The coordinator advances only where workflow_advance is offered
				// (compileRuntimeToolPlan promotes it for such a run).
				if !slices.Contains(cr.auth.Offered, "workflow_advance") {
					return guidanceRender{}, nil
				}
				runCtx.AdvanceWhenGateMet, exit.CoordinatorAdvances = "coordinator", true
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
			switch knobValue[int](knobs, set, "request") {
			case 1:
				runCtx.Request = &api.WorkflowRequestState{Status: "active", Sequence: 1}
			case 2:
				runCtx.Request = &api.WorkflowRequestState{Status: "active", Text: "Draft the plan", Source: "user", Sequence: 1}
			}
			var roster *inject.AgentRoster
			// The declared rosters name agents ResolveAgentRoster excludes with web
			// search off: a non-worker and an external-source worker.
			declared := [][]string{nil, {"skeptic", "web-researcher"}, {"skeptic", "web-researcher", "coordinator"}}[knobValue[int](knobs, set, "roster")]
			if declared != nil {
				r := inject.ResolveAgentRoster(cr.state.TurnProfile.SurfaceID, declared, cr.state.RootCount, false, false)
				roster = &r
			}
			switch knobValue[int](knobs, set, "feedback") {
			case 1:
				runCtx.PendingFeedback = &api.PendingFeedback{PhaseID: "draft"}
			case 2:
				runCtx.PendingFeedback = &api.PendingFeedback{PhaseID: "draft", Prompt: "Which scope first?"}
			}
			switch knobValue[int](knobs, set, "report") {
			case 1:
				snap.ReportDocumentEnabled = true
			case 2:
				snap.ReportDocumentEnabled = true
				snap.ReportRating = &inject.ReportRatingView{Question: "Overall risk?", Levels: "high, low", Dimensions: []string{"auth", "crypto"}, Questions: "auth: high|low"}
			}
			// Manifest transitions declare their actors (human, coordinator);
			// the coordinator may fire an arm that names it.
			switch knobValue[int](knobs, set, "choice") {
			case 1:
				exit.ChoiceTransitions = []inject.PhaseExitChoiceArm{{ID: "revise", Label: "Send back for revision", Actors: []string{"human"}}}
			case 2:
				if !slices.Contains(cr.auth.Offered, "workflow_transition") {
					return guidanceRender{}, nil
				}
				exit.ChoiceTransitions = []inject.PhaseExitChoiceArm{{ID: "revise", Label: "Send back for revision", Actors: []string{"coordinator", "human"}, CoordinatorMayFire: true}}
			}
			if knobValue[int](knobs, set, "fanout") == 1 {
				// A stamped plan renders where the phase dispatches workers.
				if !slices.Contains(cr.auth.Offered, "task") || !slices.Contains(cr.auth.Offered, "wait") {
					return guidanceRender{}, nil
				}
				snap.FanoutPlan = "- leg-1: security-reviewer on the HTTP entry points"
			}
			var gates *feedback.GateFeedbackCatalog
			if knobValue[int](knobs, set, "obligations") == 1 {
				gates = gateCfg
			}
			idx := knobValue[int](knobs, set, "rep")
			resolve, ok := resolvers[idx]
			if !ok {
				var err error
				if resolve, err = CoordinatorNamedAgentResolver(ctx, e.s, cr.state); err != nil {
					return guidanceRender{}, err
				}
				resolvers[idx] = resolve
			}
			block, err := inject.RenderActiveWorkflowInject(ctx, e.inj, cr.auth.Reader, "sess-wf-ct09", inject.CoordinatorTurnFrame{RunContext: runCtx, Roster: roster, Runtime: snap}, nil, nil, gates)
			if err != nil {
				return guidanceRender{}, err
			}
			r := coordinatorRender(block, cr)
			r.resolveNamed = resolve
			return r, nil
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
			repKnob([]string{casegen.AnyFact}, len(reps), func(i int) string { return reps[i].state.ID }),
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

// workerRoundNoticeCases queue a round notice on kick.KickEngine with the
// measurements worker.quietRoundFacts and the consumption notice produce,
// and render it on stock coordinators through RenderPendingNudge.
func workerRoundNoticeCases(_ context.Context, e *guidanceCaseEnv) ([]guidanceCase, error) {
	reps := coordinatorRepsBy(e.cReps, func(coordRep) bool { return true },
		func(cr coordRep) string {
			return offers(cr.auth, "pack_board", "worker_history", "read_findings", "wait", "worker_cancel")
		})
	inFlight := []string{kick.RoundInFlightModelCall, kick.RoundInFlightQueued, kick.RoundInFlightTools}
	knobs := []casegen.Knob{
		repKnob([]string{"profile_has_pack_board", "profile_has_worker_history", "profile_has_read_findings", "profile_has_wait", "profile_has_worker_cancel"}, len(reps), func(i int) string { return reps[i].state.ID }),
		indexKnob("worker", []string{"worker"}, []string{"named", "unnamed"}),
		indexKnob("round", []string{"round_ordinal"}, []string{"second", "none"}),
		indexKnob("work", []string{"workflow_work_id"}, []string{"bound", "none"}),
		indexKnob("tokens", []string{"tokens"}, []string{"measured", "none"}),
		indexKnob("spend", []string{"spend_under_cent", "estimated_spend_usd"}, []string{"unpriced", "under-cent", "priced"}),
		indexKnob("in_flight", []string{"in_flight"}, inFlight),
		indexKnob("tools", []string{"tools", "tools_minutes"}, []string{"named", "none"}),
		indexKnob("latest", []string{"latest_message_seq"}, []string{"none", "message"}),
	}
	// One case per notice kind: each kind renders its own arm.
	var out []guidanceCase
	for _, kind := range []string{kick.RoundNoticeConsumption, kick.RoundNoticeQuiet} {
		out = append(out, guidanceCase{
			unit: "guidance/coordinator-worker-round-notice", recipient: interactions.RecipientCoordinator, knobs: knobs,
			render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
				cr := reps[knobValue[int](knobs, set, "rep")]
				facts := kick.WorkerRoundNoticeFacts{
					JobID: "job-1", AgentType: "implementer", Kind: kind,
					ModelCalls: 12, QuietFor: 9 * time.Minute, InFlight: inFlight[knobValue[int](knobs, set, "in_flight")],
				}
				if knobValue[int](knobs, set, "worker") == 0 {
					facts.Worker = "impl-1"
				}
				if knobValue[int](knobs, set, "round") == 0 {
					facts.RoundOrdinal = 2
				}
				if knobValue[int](knobs, set, "work") == 0 {
					facts.WorkflowWorkID = "work-1"
				}
				if knobValue[int](knobs, set, "tokens") == 0 {
					facts.Tokens = 4200
				}
				switch knobValue[int](knobs, set, "spend") {
				case 1:
					facts.SpendPriced, facts.SpendNanoUSD = true, 4_000_000
				case 2:
					facts.SpendPriced, facts.SpendNanoUSD = true, 40_000_000
				}
				if facts.InFlight == kick.RoundInFlightTools {
					facts.ToolCalls, facts.ToolsDone = 3, 1
					// kickOccurrenceProducible: named tools come from the recipient's own.
					if knobValue[int](knobs, set, "tools") == 0 && slices.Contains(cr.auth.Offered, "command") {
						facts.Tools, facts.ToolsFor = []string{"command"}, 3*time.Minute
					}
				}
				if knobValue[int](knobs, set, "latest") == 1 {
					facts.LatestMessageSeq = 4
				}
				engine := &kick.KickEngine{}
				engine.SetPromptEngine(e.s.Engine())
				engine.QueueDeferred("sess-notice", "coordinator-worker-round-notice", kick.WithWorkerRoundNotice(facts))
				engine.TakePendingKickIDUnless("sess-notice", nil)
				text, _, _, err := engine.RenderPendingNudge(ctx, "sess-notice", kick.CoordinatorKickRenderContext{Reader: cr.auth.Reader})
				if err != nil {
					return guidanceRender{}, err
				}
				return coordinatorRender(text, cr), nil
			},
		})
	}
	return out, nil
}

// workerTaskFinishedCases queue the worker-finished kick with the result
// notice workerroster.Source.ResultNotice describes: the worker and its leg
// status, the result handle when the round named one, the changed paths
// (bounded, with an omitted count) and the instructions the round never
// received. They render on stock coordinators through RenderPendingNudge.
func workerTaskFinishedCases(_ context.Context, e *guidanceCaseEnv) ([]guidanceCase, error) {
	// A device-owned coordinator profile keeps every stock pack, so its
	// readers receive this kick too.
	reps := catalogRepsBy(e.cReps, func(cr coordRep) string {
		return offers(cr.auth, "promote_overlay", "instruct_worker", "preview_overlay", "worker_history", "update_progress", "wait")
	})
	knobs := []casegen.Knob{
		repKnob([]string{"profile_has_promote_overlay", "profile_has_instruct_worker", "profile_has_preview_overlay", "profile_has_worker_history", "profile_has_update_progress", casegen.AnyFact}, len(reps), func(i int) string { return reps[i].state.ID }),
		indexKnob("handle", []string{"worker_result.result_handle"}, []string{"named", "none"}),
		indexKnob("changed", []string{"worker_result.changed", "worker_result.changed_omitted"}, []string{"bounded", "shown", "none"}),
		indexKnob("not_applied", []string{"worker_result.not_applied"}, []string{"none", "one"}),
		// The roster counts results awaiting landing and partial reports
		// independently (guidancedelivery.kickRenderContext). One knob covers
		// all four combinations so sweepSettings witnesses both together.
		indexKnob("roster", []string{"pending_overlays", "partial_workers"}, []string{"none", "pending", "partial", "both"}),
	}
	return []guidanceCase{{
		unit: "guidance/coordinator-worker-task-finished", recipient: interactions.RecipientCoordinator, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			cr := reps[knobValue[int](knobs, set, "rep")]
			notice := map[string]any{"worker": "impl-1", "leg_status": "complete", "changed": []string(nil), "changed_omitted": 0, "not_applied": []string(nil)}
			if knobValue[int](knobs, set, "handle") == 0 {
				notice["result_handle"] = "res-1"
			}
			switch knobValue[int](knobs, set, "changed") {
			case 0:
				notice["changed"], notice["changed_omitted"] = []string{"a.go", "b.go"}, 1
			case 1:
				notice["changed"] = []string{"a.go"}
			}
			if knobValue[int](knobs, set, "not_applied") == 1 {
				notice["not_applied"] = []string{"Also update the docs."}
			}
			var pending, partial int
			switch knobValue[int](knobs, set, "roster") {
			case 1:
				pending = 1
			case 2:
				partial = 1
			case 3:
				pending, partial = 1, 1
			}
			engine := &kick.KickEngine{}
			engine.SetPromptEngine(e.s.Engine())
			engine.QueueDeferred("sess-finished", "coordinator-worker-task-finished", kick.WithVars(map[string]any{"worker_result": notice}))
			engine.TakePendingKickIDUnless("sess-finished", nil)
			text, _, _, err := engine.RenderPendingNudge(ctx, "sess-finished", kick.CoordinatorKickRenderContext{
				Reader: cr.auth.Reader, PendingOverlays: pending, PartialWorkers: partial,
			})
			if err != nil {
				return guidanceRender{}, err
			}
			return coordinatorRender(text, cr), nil
		},
	}}, nil
}

// scheduledWakeCases queue the timer-wake kick on kick.KickEngine and render
// it with the live roster facts the turn passes: results awaiting landing and
// the batch phase.
func scheduledWakeCases(_ context.Context, e *guidanceCaseEnv) ([]guidanceCase, error) {
	reps := coordinatorRepsBy(e.cReps, func(coordRep) bool { return true },
		func(cr coordRep) string { return offers(cr.auth, "preview_overlay") })
	knobs := []casegen.Knob{
		repKnob([]string{"profile_has_preview_overlay"}, len(reps), func(i int) string { return reps[i].state.ID }),
		{Name: "pending", Facts: []string{"pending_overlays"}, Values: []any{0, 1, 2}},
		{Name: "batch", Facts: []string{"batch_phase"}, Values: []any{"", "closed"}, Labels: []string{"open", "closed"}},
	}
	return []guidanceCase{{
		unit: "guidance/coordinator-scheduled", recipient: interactions.RecipientCoordinator, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			cr := reps[knobValue[int](knobs, set, "rep")]
			engine := &kick.KickEngine{}
			engine.SetPromptEngine(e.s.Engine())
			engine.QueueDeferred("sess-wake", "coordinator-scheduled")
			engine.TakePendingKickIDUnless("sess-wake", nil)
			text, _, _, err := engine.RenderPendingNudge(ctx, "sess-wake", kick.CoordinatorKickRenderContext{
				Reader: cr.auth.Reader, PendingOverlays: knobValue[int](knobs, set, "pending"), BatchPhase: knobValue[string](knobs, set, "batch"),
			})
			if err != nil {
				return guidanceRender{}, err
			}
			return coordinatorRender(text, cr), nil
		},
	}}, nil
}

// skillProcedureCases render inject.RenderSkillProcedureBlock for a preload
// the turn's opening decision selected (no trigger tool) and one a loadable
// tool's first call selected.
func skillProcedureCases(_ context.Context, e *guidanceCaseEnv) ([]guidanceCase, error) {
	reps := coordinatorRepsBy(e.cReps, func(coordRep) bool { return true },
		func(cr coordRep) string { return offers(cr.auth, "skills_read", "command") })
	knobs := []casegen.Knob{
		repKnob([]string{"profile_has_skills_read", casegen.AnyFact}, len(reps), func(i int) string { return reps[i].state.ID }),
		indexKnob("trigger", []string{"trigger_tool"}, []string{"tool", "decision"}),
	}
	return []guidanceCase{{
		unit: "guidance/skill-procedure", recipient: interactions.RecipientCoordinator, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			cr := reps[knobValue[int](knobs, set, "rep")]
			preload := turnload.SkillPreload{Name: "verify-a-change", Body: "Run the declared project check after each change."}
			if knobValue[int](knobs, set, "trigger") == 0 {
				// The trigger is one of the recipient's own tools.
				if len(cr.auth.Offered) == 0 {
					return guidanceRender{}, nil
				}
				preload.Tool = cr.auth.Offered[0]
			}
			text, err := inject.RenderSkillProcedureBlock(ctx, e.inj, cr.auth.Reader, "sess-skill-ct09", prompts.CoordinatorProfileID, preload)
			if err != nil {
				return guidanceRender{}, err
			}
			return coordinatorRender(text, cr), nil
		},
	}}, nil
}

// citationGroundingCases render the coordinator citation-repair kick with the
// observed-evidence report guidance.GroundingHintData builds through
// guidance.FormatOffenderReport: each list absent, within the display cap, or
// beyond it. Handles are ones the recipient mints.
func citationGroundingCases(_ context.Context, e *guidanceCaseEnv) ([]guidanceCase, error) {
	reps := coordinatorRepsBy(e.cReps, func(cr coordRep) bool { return cr.auth.Reader.Mints()["read"] },
		func(cr coordRep) string { return offers(cr.auth, "recall") })
	sizes := []int{1, 0, guidance.MaxOffenderDisplay + 2}
	sizeLabels := []string{"one", "none", "over-cap"}
	knobs := []casegen.Knob{
		repKnob([]string{"can_recall", casegen.AnyFact}, len(reps), func(i int) string { return reps[i].state.ID }),
		indexKnob("handles", []string{"observed_handles_sample", "observed_handles_omitted"}, sizeLabels),
		indexKnob("paths", []string{"observed_paths_sample", "observed_paths_omitted"}, sizeLabels),
		indexKnob("urls", []string{"observed_urls_sample", "observed_urls_omitted"}, sizeLabels),
	}
	list := func(n int, item func(int) string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = item(i + 1)
		}
		return out
	}
	return []guidanceCase{{
		unit: "guidance/coordinator-citation-grounding", recipient: interactions.RecipientCoordinator, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			cr := reps[knobValue[int](knobs, set, "rep")]
			data := guidance.OffenderHintData([]string{"ungrounded-claim"})
			for _, part := range []struct {
				knob, key string
				item      func(int) string
			}{
				{"handles", "observed_handles", func(i int) string { return fmt.Sprintf("read#%d", i) }},
				{"paths", "observed_paths", func(i int) string { return fmt.Sprintf("src/file%d.go", i) }},
				{"urls", "observed_urls", func(i int) string { return fmt.Sprintf("https://example.test/page%d", i) }},
			} {
				report := guidance.FormatOffenderReport(list(sizes[knobValue[int](knobs, set, part.knob)], part.item))
				data[part.key+"_count"], data[part.key+"_sample"], data[part.key+"_omitted"] = report.Count, report.Sample, report.Omitted
			}
			// The closeout retry sets it from the request's recall reach.
			data["can_recall"] = cr.auth.Reach.Status("recall") == toolreach.Callable
			if err := prompts.MergeCoordinatorKickVarsForTools(data, cr.auth.Reader); err != nil {
				return guidanceRender{}, err
			}
			text, err := e.s.Engine().RenderKick(ctx, cr.auth.Reader, "coordinator-citation-grounding", data)
			if err != nil {
				return guidanceRender{}, err
			}
			return coordinatorRender(text, cr), nil
		},
	}}, nil
}
