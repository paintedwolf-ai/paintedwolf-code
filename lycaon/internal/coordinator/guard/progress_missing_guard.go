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
	gc.Workflow.ReviewLoopActive = reviewLoopActive
	gc.Invocation.Tool = strings.TrimSpace(tool)
	gc.DeriveToolClassFacts()
	gc.Progress.ProgressGatedTool = progress.IsProgressGatedTool(tool)
	gc.Progress.ProgressMissing = progress.ProgressMissing(progressContent)
	if gc.Progress.ProgressGatedTool && gc.Progress.ProgressMissing && !reviewLoopActive {
		gc.PutRejectData(ProgressMissingCode, map[string]any{"tool": strings.TrimSpace(tool)})
	}
}
