package worker

import (
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/pkg/api"
)

const maxOverlayIntentSummaryRunes = 400

// EnrichOverlayIntent attaches a compact worker-intent block for preview_overlay.
func EnrichOverlayIntent(out *api.WorkerMergeResult, task *api.WorkerTask) {
	if out == nil || task == nil {
		return
	}
	intent := api.WorkerOverlayIntent{}
	if task.Result != nil {
		intent.Summary = truncateOverlayIntent(task.Result.Summary)
	}
	if task.Scope != nil {
		intent.ScopePaths = append([]string(nil), task.Scope.Normalized().Paths...)
	}
	if intent.Summary == "" {
		intent.Summary = truncateOverlayIntent(task.Brief)
	}
	if intent.Summary == "" && len(intent.ScopePaths) == 0 {
		return
	}
	out.OverlayIntent = &intent
}

func truncateOverlayIntent(s string) string {
	return runeclamp.Clamp(strings.TrimSpace(s), maxOverlayIntentSummaryRunes)
}
