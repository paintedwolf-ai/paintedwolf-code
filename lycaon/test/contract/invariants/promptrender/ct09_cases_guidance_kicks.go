package promptrender

import (
	"context"
	"slices"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/test/contract/internal/casegen"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
)

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
		repKnob("rep", []string{"profile_has_pack_board", "profile_has_worker_history", "profile_has_read_findings", "profile_has_wait", "profile_has_worker_cancel"}, len(reps), func(i int) string { return reps[i].state.ID }),
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
	reps := coordinatorRepsBy(e.cReps, func(coordRep) bool { return true },
		func(cr coordRep) string {
			return offers(cr.auth, "promote_overlay", "instruct_worker", "preview_overlay", "worker_history", "update_progress", "wait")
		})
	knobs := []casegen.Knob{
		repKnob("rep", []string{"profile_has_promote_overlay", "profile_has_instruct_worker", "profile_has_preview_overlay", "profile_has_worker_history", "profile_has_update_progress", casegen.AnyFact}, len(reps), func(i int) string { return reps[i].state.ID }),
		indexKnob("handle", []string{"worker_result.result_handle"}, []string{"named", "none"}),
		indexKnob("changed", []string{"worker_result.changed", "worker_result.changed_omitted"}, []string{"bounded", "shown", "none"}),
		indexKnob("not_applied", []string{"worker_result.not_applied"}, []string{"none", "one"}),
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
			engine := &kick.KickEngine{}
			engine.SetPromptEngine(e.s.Engine())
			engine.QueueDeferred("sess-finished", "coordinator-worker-task-finished", kick.WithVars(map[string]any{"worker_result": notice}))
			engine.TakePendingKickIDUnless("sess-finished", nil)
			text, _, _, err := engine.RenderPendingNudge(ctx, "sess-finished", kick.CoordinatorKickRenderContext{Reader: cr.auth.Reader})
			if err != nil {
				return guidanceRender{}, err
			}
			return coordinatorRender(text, cr), nil
		},
	}}, nil
}
