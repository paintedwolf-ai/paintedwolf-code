package store

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

func (s *sqlSessions) MarkSessionSeen(ctx context.Context, id string, seenAt time.Time) error {
	core := s.core
	unlock := core.lockMutation(id)
	defer unlock()
	tx, err := core.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := core.queries.WithTx(tx)
	row, err := q.GetSession(ctx, id)
	if db.IsNoRows(err) {
		return ErrSessionNotFound
	}
	if err != nil {
		return err
	}
	previous, err := db.TimePtrFromNull(row.SeenAt)
	if err != nil {
		return err
	}
	if previous != nil && !seenAt.After(*previous) {
		return nil
	}
	stamp := db.NullTimePtr(&seenAt)
	crossed, err := q.SessionSeenCrossesFinish(ctx, db.SessionSeenCrossesFinishParams{
		SessionID: id, PreviousSeenAt: row.SeenAt, SeenAt: stamp,
	})
	if err != nil {
		return err
	}
	if err := q.MarkSessionSeen(ctx, db.MarkSessionSeenParams{ID: id, SeenAt: stamp}); err != nil {
		return err
	}
	if crossed != 0 {
		sess, err := sessionFromRow(row)
		if err != nil {
			return err
		}
		if err := enqueueSessionEvent(ctx, core, tx, sess, api.SessionEventActionUpdated); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if crossed != 0 {
		core.outbox.Notify()
	}
	return nil
}

func (s *memorySessions) MarkSessionSeen(ctx context.Context, id string, seenAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	core := s.core
	core.mu.Lock()
	defer core.mu.Unlock()
	sess, ok := core.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	if sess.SeenAt == nil || seenAt.After(*sess.SeenAt) {
		utc := seenAt.UTC()
		sess.SeenAt = &utc
	}
	return nil
}
