package assembly

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/pkg/api"
)

// resolveCoordinatorProfile returns the profile and the state used to select it.
func (e *AssemblyEngine) resolveCoordinatorProfile(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	history []api.Message,
	turn *TurnAssemblyScratch,
) (surface.TurnProfile, surface.ImplementSessionState) {
	implState := e.implementSessionState(ctx, sess)
	if turn != nil {
		if pinned := strings.TrimSpace(turn.SurfaceID); pinned != "" {
			return surface.ResolveTurnProfileForSurface(pinned, frame.RunContext, sess), implState
		}
	}
	return surface.ResolveTurnProfile(frame.RunContext, sess, history, implState), implState
}
