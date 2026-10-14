package compaction

import (
	"context"

	"github.com/lycaon/lycaon/internal/messageview"
)

// ContinuationRanker orders eligible references; coverage and retention remain host-owned.
type ContinuationRanker interface {
	RankContinuation(context.Context, SessionInfo, []messageview.ContinuationSource) ([]string, error)
}

// WithContinuationRanker installs optional detail ranking without changing the baseline.
func (c *SimpleCompactor) WithContinuationRanker(ranker ContinuationRanker) *SimpleCompactor {
	out := c.WithConfig(c.cfg)
	out.ranker = ranker
	return out
}

func (c *SimpleCompactor) rankContinuation(ctx context.Context, info SessionInfo, record *messageview.Continuation) {
	if c.ranker == nil || len(record.Sources) < 2 {
		return
	}
	sources := append([]messageview.ContinuationSource(nil), record.Sources...)
	for i := range sources {
		sources[i].Handles = append([]string(nil), sources[i].Handles...)
	}
	ids, err := c.ranker.RankContinuation(ctx, info, sources)
	if err != nil || len(ids) != len(record.Sources) {
		return
	}
	byID := make(map[string]messageview.ContinuationSource, len(record.Sources))
	for _, source := range record.Sources {
		byID[source.MessageID] = source
	}
	ranked := make([]messageview.ContinuationSource, 0, len(ids))
	for _, id := range ids {
		source, ok := byID[id]
		if !ok {
			return
		}
		ranked = append(ranked, source)
		delete(byID, id)
	}
	// Rendering takes the trailing references, so highest priority belongs last.
	record.Sources = ranked
}
