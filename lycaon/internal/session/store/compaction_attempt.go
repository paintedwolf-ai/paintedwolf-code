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
