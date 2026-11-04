package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

// ObserveCoordinatorHostNoToolTurn publishes post-turn host-shape facts. draft is
// the turn's typed read on a report surface and zero elsewhere.
func ObserveCoordinatorHostNoToolTurn(
	sess *api.Session,
	history []api.Message,
	lastAssistant string,
	draft guidance.CloseoutDraft,
	turnTools []string,
	surfaceID string,
	workersIdle bool,
	implState surface.ImplementSessionState,
	batchTurn BatchTurnGuard,
	gc *oar.GuardContext,
) {
	if gc == nil || sess == nil || sess.IsWorkerChild() {
		return
	}
	lastAssistant = strings.TrimSpace(lastAssistant)
	gc.Workers.WorkersIdle = workersIdle
	gc.Session.IsHostCycleTurn = surface.HostCycleTurn(history)
	gc.Session.LastAssistant = lastAssistant
	gc.Session.TurnTools = append([]string(nil), turnTools...)
	gc.Session.Surface = strings.TrimSpace(surfaceID)
	gc.Workflow.BatchPhase = implState.BatchPhase
	gc.Workflow.BatchClosed = batchAlreadyClosed(batchTurn, implState.BatchPhase)
	gc.Progress.ProgressHasOpenSteps = implState.ProgressOpenCount > 0
	pendingOverlay := len(implState.PendingOverlayIDs) > 0
	gc.Workers.PendingOverlayPromote = pendingOverlay
	gc.Session.SurfaceMayFinish = SurfaceFinishesWithUserProse(surfaceID)
	gc.Session.CloseoutSurface = surface.SurfaceDeliversReport(surfaceID)
	if lastAssistant != "" {
		gc.Progress.CloseoutHasAnswer = draft.HasAnswer()
		gc.Progress.TaskEnvelopeEcho = assistantEchoesTaskEnvelope(lastAssistant)
	}
}
