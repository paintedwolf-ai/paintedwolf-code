package reenter

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
)

// CoordinatorNudger schedules coordinator loop wakes after manifest same-phase re-enter.
type CoordinatorNudger interface {
	Nudge(ctx context.Context, sessionID string, wake, inform anchor.ID, legID string, env anchor.Envelope)
}

// NudgeOnManifestReenter schedules a leg-finished loop wake for same-phase manifest re-enter
// when on_reenter declares reenter_leg without worker-task-finished inject_kick.
// worker-task-finished inject is kick-only (PhaseReenterHook); SessionOutcomeBridge schedules
// the coordinator wake via ShouldNudgeAfterWorkerTask.
func NudgeOnManifestReenter(
	ctx context.Context,
	n CoordinatorNudger,
	sessionID string,
	manifest workflowdef.Manifest,
	previousPhase, newPhase string,
) {
	if n == nil {
		return
	}
	legID, ok := workflowphases.ReenterLegForAdvance(manifest, previousPhase, newPhase, sessionID)
	if !ok {
		return
	}
	def, hasDef := manifest.PhaseByID(newPhase)
	if hasDef && anchor.SameInform(def.OnReenter.InjectKick, anchor.WorkerTaskFinished) {
		return
	}
	n.Nudge(ctx, sessionID, anchor.LegFinished, anchor.LegFinished, legID, anchor.Envelope{})
}
