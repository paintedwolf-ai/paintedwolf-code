package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/spawn"
)

const ProgressSynthesisReconcileOnlyCode = "PROGRESS_SYNTHESIS_RECONCILE_ONLY"

// ObserveProgressReconcileOnSynthesis publishes synthesis progress-update facts.
func ObserveProgressReconcileOnSynthesis(
	surfaceID string,
	currentProgress string,
	args map[string]any,
	gc *oar.GuardContext,
) {
	if gc == nil || gc.Invocation.Tool != "update_progress" {
		return
	}
	gc.Session.Surface = strings.TrimSpace(surfaceID)
	if gc.Session.Surface != spawn.SurfaceImplementSynthesis {
		return
	}
	proposed, _ := args["content"].(string)
	proposed = strings.TrimSpace(proposed)
	if proposed == "" {
		return
	}
	gc.Progress.ProgressReconcileNeeded = !progress.SynthesisReconcileOnly(currentProgress, proposed)
}
