package compaction

import (
	"context"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/pkg/api"
)

// Baseline uses deterministic projections without making provider calls.
func (c *SimpleCompactor) Baseline() *SimpleCompactor {
	return NewSimpleCompactor(c.cfg, nil).WithContinuationRanker(c.ranker)
}

// EnrichContinuation synthesizes a captured source page without publishing it.
func (c *SimpleCompactor) EnrichContinuation(ctx context.Context, info SessionInfo, rows []api.Message, record *messageview.Continuation) (*messageview.Continuation, error) {
	selected := make(map[string]bool, len(record.Sources))
	for _, source := range record.Sources {
		selected[source.MessageID] = true
	}
	var prefix []ContextMessage
	for _, row := range ContextMessagesFromAPI(rows) {
		if selected[row.ID] {
			prefix = append(prefix, row)
		}
	}
	summary, sources, err := c.summarizePrefix(ctx, info, prefix)
	accepted := false
	for _, source := range sources {
		accepted = accepted || source.Disposition != "reference"
	}
	if err != nil && !accepted {
		return nil, err
	}
	enriched := *record
	enriched.Summary = summaryRecord(summary)
	enriched.Sources = mergeContinuationSources(record, sources)
	enriched.Reason = "summarized"
	if err != nil {
		enriched.Reason = "references_only"
	}
	for _, source := range sources {
		if source.Disposition != "reference" {
			enriched.SummaryThroughOrd = max(enriched.SummaryThroughOrd, source.Ord)
		}
	}
	c.rankContinuation(ctx, info, &enriched)
	return &enriched, nil
}
