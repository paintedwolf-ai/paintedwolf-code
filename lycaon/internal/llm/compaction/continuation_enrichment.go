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
func (c *SimpleCompactor) EnrichContinuation(ctx context.Context, info SessionInfo, rows []api.Message, record *messageview.Continuation, beforeCall func(context.Context) error) (*messageview.Continuation, error) {
	info.PriorContinuation = record
	prefix := ContextMessagesFromAPI(rows)
	summary, sources, err := c.summarizePrefix(ctx, info, prefix, beforeCall)
	return applySynthesisResult(record, summary, sources, err)
}

func applySynthesisResult(record *messageview.Continuation, summary compactionSummary, sources []messageview.ContinuationSource, err error) (*messageview.Continuation, error) {
	enriched := messageview.CloneContinuation(record)
	dispositions := make(map[string]string)
	for _, source := range sources {
		if source.Disposition != "reference" {
			dispositions[source.MessageID] = source.Disposition
			enriched.SummaryThroughOrd = max(enriched.SummaryThroughOrd, source.Ord)
		}
	}
	if len(dispositions) == 0 && err != nil {
		return nil, err
	}
	for i, source := range enriched.Sources {
		if disposition, ok := dispositions[source.MessageID]; ok {
			enriched.Sources[i].Disposition = disposition
		}
	}
	enriched.Summary = summaryRecord(summary)
	enriched.Reason = "summarized"
	if err != nil || enriched.SummaryThroughOrd < enriched.ThroughOrd {
		enriched.Reason = "references_only"
	}
	return enriched, nil
}
