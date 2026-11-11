package store

import (
	"context"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/messageview"
)

func (s *sqlCompaction) GetCompactionAttempt(ctx context.Context, sessionID string) (messageview.CompactionAttempt, bool, error) {
	row, err := s.core.queries.GetCompactionAttempt(ctx, sessionID)
	if db.IsNoRows(err) {
		return messageview.CompactionAttempt{}, false, nil
	}
	return messageview.CompactionAttempt{Revision: row.Revision, Reason: row.Reason}, err == nil, err
}

func (s *sqlCompaction) PutCompactionAttempt(ctx context.Context, sessionID string, attempt messageview.CompactionAttempt) error {
	return s.core.queries.PutCompactionAttempt(ctx, db.PutCompactionAttemptParams{SessionID: sessionID, Revision: attempt.Revision, Reason: attempt.Reason})
}

func (s *memoryCompaction) GetCompactionAttempt(ctx context.Context, sessionID string) (messageview.CompactionAttempt, bool, error) {
	if err := ctx.Err(); err != nil {
		return messageview.CompactionAttempt{}, false, err
	}
	s.core.mu.RLock()
	defer s.core.mu.RUnlock()
	attempt, ok := s.core.compactionAttempts[sessionID]
	return attempt, ok, nil
}

func (s *memoryCompaction) PutCompactionAttempt(ctx context.Context, sessionID string, attempt messageview.CompactionAttempt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	core := s.core
	core.mu.Lock()
	defer core.mu.Unlock()
	if _, ok := core.sessions[sessionID]; !ok {
		return ErrSessionNotFound
	}
	if core.compactionAttempts == nil {
		core.compactionAttempts = make(map[string]messageview.CompactionAttempt)
	}
	core.compactionAttempts[sessionID] = attempt
	return nil
}
