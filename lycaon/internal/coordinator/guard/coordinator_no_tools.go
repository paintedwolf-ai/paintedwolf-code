package guard

import (
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

// CoordinatorEvidenceOptional exempts the first visible turn when no tools ran.
func CoordinatorEvidenceOptional(history []api.Message, turnTools []string) bool {
	if len(turnTools) > 0 {
		return false
	}
	since := api.UserIntentBoundary(history)
	for i := since; i < len(history); i++ {
		if history[i].Role == api.MessageRoleTool {
			return false
		}
	}
	// Internal messages do not count as prior user turns.
	if since <= 0 {
		return true
	}
	return api.UserIntentBoundary(history[:since-1]) == 0
}

// PrepareCoordinatorCloseoutContent stores a report surface's closeout draft
// as its envelope. A fence-only repair joins the pinned body. The read names
// the draft's members the report did not take; the closeout refuses them.
func PrepareCoordinatorCloseoutContent(surfaceID, content, pinnedSynthesis string) (string, guidance.CloseoutRead, bool) {
	if !surface.SurfaceDeliversReport(surfaceID) {
		return content, guidance.CloseoutRead{}, false
	}
	read, ok := guidance.ReadCloseoutReport(content, pinnedSynthesis)
	if !ok {
		return content, guidance.CloseoutRead{}, false
	}
	raw, err := guidance.MarshalCoordinatorCompletionReport(read.Report)
	if err != nil {
		return content, guidance.CloseoutRead{}, false
	}
	return raw, read, true
}
