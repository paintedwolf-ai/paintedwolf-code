package compaction

import (
	"encoding/json"
	"slices"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/internal/tokenest"
)

func appendSummaryPage(summary, page compactionSummary) compactionSummary {
	summary.Facts = appendSummaryItems(summary.Facts, page.Facts, 16)
	summary.Completed = appendSummaryItems(summary.Completed, page.Completed, 10)
	summary.Pending = appendSummaryItems(summary.Pending, page.Pending, 10)
	summary.Reacquire = appendSummaryItems(summary.Reacquire, page.Reacquire, 16)
	summary.Constraints = appendSummaryItems(summary.Constraints, page.Constraints, 10)
	summary.SourceContext = sourceref.Merge(summary.SourceContext, page.SourceContext)
	return summary
}

func appendSummaryItems(accepted, page []string, limit int) []string {
	items := slices.Clone(accepted)
	for _, item := range page {
		if len(items) >= limit {
			break
		}
		if !slices.Contains(items, item) {
			items = append(items, item)
		}
	}
	return items
}

// synthesisContext preserves the page budget when a long accepted record is reused.
func synthesisContext(record *messageview.Continuation, budget int) *messageview.Continuation {
	if record == nil {
		return nil
	}
	prior := priorSummary(record)
	for limit := 16; limit > 0; limit-- {
		clues := messageview.ContinuationSummary{
			Facts:       prior.Facts[:min(limit, len(prior.Facts))],
			Completed:   prior.Completed[:min(limit, len(prior.Completed))],
			Pending:     prior.Pending[:min(limit, len(prior.Pending))],
			Reacquire:   prior.Reacquire[:min(limit, len(prior.Reacquire))],
			Constraints: prior.Constraints[:min(limit, len(prior.Constraints))],
		}
		raw, _ := json.Marshal(clues) //nolint:errchkjson // Clues contain only strings.
		if tokenest.FromUnitCount(len(raw), tokenest.DefaultDivisor) <= budget {
			return &messageview.Continuation{Summary: clues}
		}
	}
	return nil
}
