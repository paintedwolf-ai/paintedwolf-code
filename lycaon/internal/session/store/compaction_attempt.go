package store

import (
	"context"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/messageview"
)

func (s *SQL) GetCompactionAttempt(ctx context.Context, sessionID string) (messageview.CompactionAttempt, bool, error) {
	row, err := s.queries.GetCompactionAttempt(ctx, sessionID)
	if db.IsNoRows(err) {
		return messageview.CompactionAttempt{}, false, nil
	}
	return messageview.CompactionAttempt{Revision: row.Revision, Reason: row.Reason}, err == nil, err
}

func (s *SQL) PutCompactionAttempt(ctx context.Context, sessionID string, attempt messageview.CompactionAttempt) error {
	return s.queries.PutCompactionAttempt(ctx, db.PutCompactionAttemptParams{SessionID: sessionID, Revision: attempt.Revision, Reason: attempt.Reason})
}

func (s *Memory) GetCompactionAttempt(ctx context.Context, sessionID string) (messageview.CompactionAttempt, bool, error) {
	if err := ctx.Err(); err != nil {
		return messageview.CompactionAttempt{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	attempt, ok := s.compactionAttempts[sessionID]
	return attempt, ok, nil
}

func (s *Memory) PutCompactionAttempt(ctx context.Context, sessionID string, attempt messageview.CompactionAttempt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return ErrSessionNotFound
	}
	if s.compactionAttempts == nil {
		s.compactionAttempts = make(map[string]messageview.CompactionAttempt)
	}
	s.compactionAttempts[sessionID] = attempt
	return nil
}
