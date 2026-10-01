package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/pkg/api"
)

const ProgressItemNotClosedCode = "PROGRESS_ITEM_NOT_CLOSED"

// ProgressClosureBaseline snapshots the checklist when the closure latch arms.
// The latch clears on a closed-count advance or any content change since arm.
type ProgressClosureBaseline struct {
	Closed  int    // done + n/a rows at arm time
	Content string // stored checklist bytes at arm time
	// Settled names the successful finishes the checklist has not absorbed.
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
	gc.ProgressClosureArmed = armed
	gc.Tool = strings.TrimSpace(tool)
	gc.DeriveToolClassFacts()
	gc.ProgressGatedTool = progress.IsProgressGatedTool(tool)
	if !armed || !gc.ProgressGatedTool {
		return
	}
	done, pending, na := progress.CloseCounts(progressContent)
	gc.ProgressOpenItems = int64(pending)
	closed := done + na
	gc.ProgressClosedBeyondBaseline = closed > baseline.Closed
	gc.ProgressReconciledSinceArm = progressContent != baseline.Content
	if pending > 0 && !gc.ProgressClosedBeyondBaseline && !gc.ProgressReconciledSinceArm {
		gc.PutRejectData(ProgressItemNotClosedCode, map[string]any{
			"tool":         strings.TrimSpace(tool),
			"pending":      pending,
			"settled_work": append([]string(nil), baseline.Settled...),
		})
	}
}
