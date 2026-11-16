package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
)

const CoordinatorHostTurnRequiresWaitCode = "COORDINATOR_HOST_TURN_REQUIRES_WAIT"

// SurfaceFinishesWithUserProse reports a surface the catalog lets close with grounded prose.
func SurfaceFinishesWithUserProse(surfaceID string) bool {
	catalog, err := surfacecatalog.Load()
	if err != nil {
		return false
	}
	row, err := catalog.Surface(strings.TrimSpace(surfaceID))
	return err == nil && row.ProseFinish
}

// HostTurnMayFinishWithProse requires an idle roster and no pending promotion.
func HostTurnMayFinishWithProse(surfaceID string, workersIdle, pendingOverlayPromote bool) bool {
	return workersIdle && !pendingOverlayPromote && SurfaceFinishesWithUserProse(surfaceID)
}
