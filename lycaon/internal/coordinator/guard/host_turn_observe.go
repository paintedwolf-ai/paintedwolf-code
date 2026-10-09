package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

// ObserveCoordinatorHostNoToolTurn publishes post-turn host-shape facts.
func ObserveCoordinatorHostNoToolTurn(
	sess *api.Session,
	history []api.Message,
	lastAssistant string,
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
		_, ok := guidance.ParseCoordinatorCompletionReport(lastAssistant)
		gc.Progress.HasCompletionReport = ok
		gc.Progress.TaskEnvelopeEcho = assistantEchoesTaskEnvelope(lastAssistant)
	}
}
