package store

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/pkg/api"
)

// PutCompactionView atomically publishes a fresh view and its next generation.
func (s *memoryCompaction) PutCompactionView(ctx context.Context, sessionID string, view CompactionView) error {
	if len(view.Messages) > messageview.MaxCompactionRows {
		return fmt.Errorf("compaction projection exceeds its row budget")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	core := s.core
	core.mu.Lock()
	defer core.mu.Unlock()
	if _, ok := core.sessions[sessionID]; !ok {
		return ErrSessionNotFound
	}
	if view.Generation != core.sessions[sessionID].CompactionGeneration+1 {
		return fmt.Errorf("compaction generation changed")
	}
	if err := validateMemoryCompactionSource(core.messages[sessionID], core.transcriptSeq[sessionID], view); err != nil {
		return err
	}
	cp := view
	cp.Messages = append([]api.Message(nil), view.Messages...)
	cp.Continuation = messageview.CloneContinuation(view.Continuation)
	cp.CreatedAt = time.Now().UTC()
	core.compactionViews[sessionID] = cp
	core.sessions[sessionID].CompactionGeneration = view.Generation
	core.sessions[sessionID].UpdatedAt = cp.CreatedAt
	return nil
}

// GetCompactionView returns the session's compacted view, ok=false when none exists.
func (s *memoryCompaction) GetCompactionView(ctx context.Context, sessionID string) (*CompactionView, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	core := s.core
	core.mu.RLock()
	defer core.mu.RUnlock()
	v, ok := core.compactionViews[sessionID]
	if !ok {
		return nil, false, nil
	}
	cp := v
	cp.Messages = append([]api.Message(nil), v.Messages...)
	cp.Continuation = messageview.CloneContinuation(v.Continuation)
	return &cp, true, nil
}

// CompactionViewCurrent reports whether a covered row changed after the view.
func (s *memoryCompaction) CompactionViewCurrent(ctx context.Context, sessionID string, coveredThroughOrd int64, coveredThroughID string, sourceSeq int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	core := s.core
	core.mu.RLock()
	defer core.mu.RUnlock()
	if _, ok := core.sessions[sessionID]; !ok {
		return false, ErrSessionNotFound
	}
	view := CompactionView{CoveredThroughOrd: coveredThroughOrd, CoveredThroughID: coveredThroughID, SourceSeq: sourceSeq}
	return validateMemoryCompactionSource(core.messages[sessionID], core.transcriptSeq[sessionID], view) == nil, nil
}
