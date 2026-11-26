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
		{Name: "kind", Facts: []string{"notice_kind"}, Values: []any{kick.RoundNoticeConsumption, kick.RoundNoticeQuiet}},
		indexKnob("worker", []string{"worker"}, []string{"named", "unnamed"}),
		indexKnob("round", []string{"round_ordinal"}, []string{"second", "none"}),
		indexKnob("work", []string{"workflow_work_id"}, []string{"bound", "none"}),
		indexKnob("tokens", []string{"tokens"}, []string{"measured", "none"}),
		indexKnob("spend", []string{"spend_under_cent", "estimated_spend_usd"}, []string{"unpriced", "under-cent", "priced"}),
		indexKnob("in_flight", []string{"in_flight"}, inFlight),
		indexKnob("tools", []string{"tools", "tools_minutes"}, []string{"named", "none"}),
		indexKnob("latest", []string{"latest_message_seq"}, []string{"none", "message"}),
	}
	return []guidanceCase{{
		unit: "guidance/coordinator-worker-round-notice", recipient: interactions.RecipientCoordinator, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			cr := reps[knobValue[int](knobs, set, "rep")]
			facts := kick.WorkerRoundNoticeFacts{
				JobID: "job-1", AgentType: "implementer", Kind: knobValue[string](knobs, set, "kind"),
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
	}}, nil
}
