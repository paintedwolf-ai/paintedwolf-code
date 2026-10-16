package history

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/pkg/api"
)

// loadActiveWorkingSet restores bounded canonical exchanges behind a published view.
func (m *Service) loadActiveWorkingSet(ctx context.Context, sess *api.Session, cfg compaction.CompactionConfig, covered int64) ([]api.Message, error) {
	tail, err := m.readBoundedCanonicalTail(ctx, sess.ID, m.canonicalTailBounds(cfg))
	if err != nil {
		return nil, err
	}
	tail, err = m.withCurrentRequest(ctx, sess, tail)
	if err != nil {
		return nil, err
	}
	current, ok := api.LastUserIntentMessage(tail)
	if !ok || current.Ord <= 0 || current.Ord > covered {
		return nil, nil
	}
	for i := range tail {
		if tail[i].ID == current.ID {
			return tail[i:], nil
		}
	}
	return nil, nil
}
