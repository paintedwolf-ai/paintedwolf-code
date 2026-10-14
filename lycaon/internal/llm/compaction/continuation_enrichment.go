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
	if err != nil {
		return nil, err
	}
	enriched := *record
	enriched.Summary = summaryRecord(summary)
	enriched.Sources = sources
	enriched.Reason = "summarized"
	c.rankContinuation(ctx, info, &enriched)
	return &enriched, nil
}

// RenderContinuation chooses a receiver-sized representation of accepted data.
func RenderContinuation(ctx context.Context, record *messageview.Continuation, recall bool) (string, error) {
	return renderCoveredContinuation(ctx, record, recall)
}
