package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/pkg/api"
)

const ProgressReconciliationRequiredCode = "PROGRESS_RECONCILIATION_REQUIRED"

// ProgressClosureBaseline snapshots the checklist when the closure latch arms.
// Only the reconciliation service acknowledges finished worker jobs.
type ProgressClosureBaseline struct {
	Revision string
	JobIDs   []string
	// Settled[i] names the finish of JobIDs[i] for the coordinator, by worker.
	Settled []string
}

// SettledWork lists the named finishes.
func (b ProgressClosureBaseline) SettledWork() []string {
	out := make([]string, 0, len(b.Settled))
	for _, entry := range b.Settled {
		if entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

// ObserveProgressReconciliationBeforeDispatch publishes progress-closure facts.
func ObserveProgressReconciliationBeforeDispatch(
	sess *api.Session,
	progressContent, tool string,
	baseline ProgressClosureBaseline,
	armed bool,
	gc *oar.GuardContext,
) {
	if gc == nil || sess == nil || sess.IsWorkerChild() {
		return
	}
	gc.Progress.ProgressClosureArmed = armed
	gc.Invocation.Tool = strings.TrimSpace(tool)
	gc.DeriveToolClassFacts()
	gc.Progress.ProgressGatedTool = progress.IsProgressGatedTool(tool)
	if !armed || !gc.Progress.ProgressGatedTool {
		return
	}
	_, pending, _ := progress.CloseCounts(progressContent)
	gc.Progress.ProgressOpenItems = int64(pending)
	gc.PutRejectData(ProgressReconciliationRequiredCode, map[string]any{
		"tool":         strings.TrimSpace(tool),
		"pending":      pending,
		"settled_work": baseline.SettledWork(),
	})
}
