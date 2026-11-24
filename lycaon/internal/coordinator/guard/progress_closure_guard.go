package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/pkg/api"
)

const ProgressReconciliationRequiredCode = "PROGRESS_RECONCILIATION_REQUIRED"

// ProgressClosureBaseline records settled worker finishes awaiting acknowledgement.
type ProgressClosureBaseline struct {
	JobIDs      []string
	Settled     []string
	Unaccounted []progress.UnaccountedResult
}

// SettledWork lists human-readable worker descriptions for latched finishes.
func (b ProgressClosureBaseline) SettledWork() []string {
	out := make([]string, 0, len(b.Settled))
	for _, entry := range b.Settled {
		if entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

// UnaccountedHandles lists result handles awaiting acknowledgement.
func (b ProgressClosureBaseline) UnaccountedHandles() []string {
	out := make([]string, 0, len(b.Unaccounted))
	for _, u := range b.Unaccounted {
		if u.Handle != "" {
			out = append(out, u.Handle)
		}
	}
	return out
}

// ObserveProgressReconciliationBeforeDispatch publishes progress-closure facts.
func ObserveProgressReconciliationBeforeDispatch(
	sess *api.Session,
	openSteps int,
	tool string,
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
	gc.Progress.ProgressOpenItems = int64(openSteps)
	gc.PutRejectData(ProgressReconciliationRequiredCode, map[string]any{
		"tool":                strings.TrimSpace(tool),
		"pending":             openSteps,
		"settled_work":        baseline.SettledWork(),
		"unaccounted_handles": baseline.UnaccountedHandles(),
	})
}
