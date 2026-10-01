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
	gc.WorkersIdle = workersIdle
	gc.IsHostCycleTurn = surface.HostCycleTurn(history)
	gc.LastAssistant = lastAssistant
	gc.TurnTools = append([]string(nil), turnTools...)
	gc.Surface = strings.TrimSpace(surfaceID)
	gc.BatchPhase = implState.BatchPhase
	gc.BatchClosed = batchAlreadyClosed(batchTurn, implState.BatchPhase)
	gc.ProgressHasOpenSteps = implState.ProgressOpenCount > 0
	pendingOverlay := len(implState.PendingOverlayIDs) > 0
	gc.PendingOverlayPromote = pendingOverlay
	gc.SurfaceMayFinish = SurfaceFinishesWithUserProse(surfaceID)
	gc.CloseoutSurface = surface.SurfaceDeliversReport(surfaceID)
	if lastAssistant != "" {
		_, ok := guidance.ParseCoordinatorCompletionReport(lastAssistant)
		gc.HasCompletionReport = ok
		gc.TaskEnvelopeEcho = assistantEchoesTaskEnvelope(lastAssistant)
	}
}
