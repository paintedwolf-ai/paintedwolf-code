package inject

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/pongoplain"
)

// Render budgets bound every injected collection.
const (
	// MaxScopeOutlineSymbols bounds one scoped file's structural outline.
	MaxScopeOutlineSymbols = 40
	// MaxScopeFileOrientations bounds how many scoped files carry an outline.
	MaxScopeFileOrientations = 24
	// MaxScopePaths bounds the suggested paths restated to the worker.
	MaxScopePaths = 64
	// MaxCharterItems bounds one model-authored charter list.
	MaxCharterItems = 24
	// MaxForwardedAttachments bounds the attachments restated to the worker.
	MaxForwardedAttachments = 24
	// MaxRecordedVerdicts bounds the run's stamped review_loop verdicts carried
	// into one assignment.
	MaxRecordedVerdicts = 8
	// MaxRecordedVerdictFields bounds one carried verdict's members.
	MaxRecordedVerdictFields = 16
)

// renderCeiling is the hard limit every budget above must stay under.
const renderCeiling = pongoplain.MaxCollectionItems

// renderBudgets lists the budgets checked against the ceiling.
func renderBudgets() map[string]int {
	return map[string]int{
		"MaxScopeOutlineSymbols":   MaxScopeOutlineSymbols,
		"MaxScopeFileOrientations": MaxScopeFileOrientations,
		"MaxScopePaths":            MaxScopePaths,
		"MaxCharterItems":          MaxCharterItems,
		"MaxForwardedAttachments":  MaxForwardedAttachments,
		"MaxRecordedVerdicts":      MaxRecordedVerdicts,
		"MaxRecordedVerdictFields": MaxRecordedVerdictFields,
	}
}

// bound truncates items to budget and reports how many it dropped.
func bound[T any](items []T, budget int) ([]T, int) {
	if budget <= 0 || len(items) <= budget {
		return items, 0
	}
	return items[:budget], len(items) - budget
}

// elisionNote names one shortened list and how much of it the worker sees.
func elisionNote(what string, shown, elided int) string {
	return fmt.Sprintf("%s — showing %d of %d; %d not listed here", what, shown, shown+elided, elided)
}
