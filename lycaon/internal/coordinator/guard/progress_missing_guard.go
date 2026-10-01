package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/pkg/api"
)

const ProgressMissingCode = "PROGRESS_MISSING"

// ObserveProgressMissingBeforeDispatch publishes progress-missing facts.
func ObserveProgressMissingBeforeDispatch(
	sess *api.Session,
	progressContent, tool string,
	reviewLoopActive bool,
	gc *oar.GuardContext,
) {
	if gc == nil || sess == nil || sess.IsWorkerChild() {
		return
	}
	gc.ReviewLoopActive = reviewLoopActive
	gc.Tool = strings.TrimSpace(tool)
	gc.DeriveToolClassFacts()
	gc.ProgressGatedTool = progress.IsProgressGatedTool(tool)
	gc.ProgressMissing = progress.ProgressMissing(progressContent)
	if gc.ProgressGatedTool && gc.ProgressMissing && !reviewLoopActive {
		gc.PutRejectData(ProgressMissingCode, map[string]any{"tool": strings.TrimSpace(tool)})
	}
}
