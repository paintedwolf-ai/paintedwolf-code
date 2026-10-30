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
	Closed   int    // done + n/a rows at arm time
	Content  string // stored checklist bytes at arm time
	Revision string
	JobIDs   []string
	// Settled names worker finishes for rejection details.
	Settled []string
}

// ObserveProgressItemNotClosedBeforeDispatch publishes progress-closure facts.
func ObserveProgressItemNotClosedBeforeDispatch(
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
	done, pending, na := progress.CloseCounts(progressContent)
	gc.Progress.ProgressOpenItems = int64(pending)
	closed := done + na
	gc.Progress.ProgressClosedBeyondBaseline = closed > baseline.Closed
	// Document changes do not consume worker-finish obligations.
	gc.Progress.ProgressReconciledSinceArm = progressContent != baseline.Content
	gc.PutRejectData(ProgressReconciliationRequiredCode, map[string]any{
		"tool":         strings.TrimSpace(tool),
		"pending":      pending,
		"settled_work": append([]string(nil), baseline.Settled...),
	})
}
