package compaction

import (
	"context"
	"sort"

	"github.com/lycaon/lycaon/internal/messageview"
)

const maxContinuationSources = 80

func mergeContinuationSources(prior *messageview.Continuation, current []messageview.ContinuationSource) []messageview.ContinuationSource {
	byID := make(map[string]messageview.ContinuationSource)
	if prior != nil {
		for _, row := range prior.Sources {
			byID[row.MessageID] = row
		}
	}
	for _, row := range current {
		byID[row.MessageID] = row
	}
	rows := make([]messageview.ContinuationSource, 0, len(byID))
	for _, row := range byID {
		rows = append(rows, row)
	}
	less := func(a, b messageview.ContinuationSource) bool {
		if a.Ord != b.Ord {
			return a.Ord < b.Ord
		}
		return a.MessageID < b.MessageID
	}
	sort.Slice(rows, func(i, j int) bool { return less(rows[i], rows[j]) })
	if len(rows) <= maxContinuationSources {
		return rows
	}
	selected := make(map[string]bool)
	var out []messageview.ContinuationSource
	for i := len(rows) - 1; i >= 0 && len(out) < maxContinuationSources/2; i-- {
		row := rows[i]
		if len(row.Handles) > 0 || row.Tool != "" {
			selected[row.MessageID] = true
			out = append(out, row)
		}
	}
	for i := len(rows) - 1; i >= 0 && len(out) < maxContinuationSources; i-- {
		if !selected[rows[i].MessageID] {
			out = append(out, rows[i])
		}
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

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
