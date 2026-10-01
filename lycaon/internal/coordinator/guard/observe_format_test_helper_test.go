package guard

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
)

// formatObservationReject runs observe then EvaluateBlock + Format (unit tests).
func formatObservationReject(t *testing.T, rejectFmt *guidance.StaticRejectFormatter, observe func(gc *oar.GuardContext)) (string, bool) {
	t.Helper()
	gc := oar.NewGuardContext()
	observe(gc)
	return formatFirstOARBlock(gc, rejectFmt,
		oar.AnchorCoordinatorPreInvoke, oar.AnchorToolPreInvoke, oar.AnchorCoordinatorCloseoutCheck)
}
