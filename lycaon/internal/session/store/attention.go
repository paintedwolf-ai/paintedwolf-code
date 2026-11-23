package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/attention"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/timelayout"
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
	crossed, err := q.SessionSeenCrossesLatestFinish(ctx, db.SessionSeenCrossesLatestFinishParams{
		SessionID: id, PreviousSeenAt: row.SeenAt, SeenAt: stamp,
	})
	if err != nil {
		return err
	}
	if err := q.MarkSessionSeen(ctx, db.MarkSessionSeenParams{
		ID: id, SeenAt: stamp, UpdatedAt: timelayout.Format(time.Now()),
	}); err != nil {
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

// ListAttentionCandidates selects unarchived root sessions with active, waiting, or unread work.
func (s *sqlSessions) ListAttentionCandidates(ctx context.Context) ([]attention.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.core.queries.ListAttentionCandidateSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list attention candidates: %w", err)
	}
	out := make([]attention.Candidate, 0, len(rows))
	for _, row := range rows {
		statusSince, err := db.ParseTime(row.StatusSince)
		if err != nil {
			return nil, fmt.Errorf("attention candidate %s status_since: %w", row.ID, err)
		}
		seenAt, err := db.TimePtrFromNull(row.SeenAt)
		if err != nil {
			return nil, fmt.Errorf("attention candidate %s seen_at: %w", row.ID, err)
		}
		out = append(out, attention.Candidate{
			SessionID:   row.ID,
			ProjectID:   row.ProjectID,
			Title:       strings.TrimSpace(row.Title.String),
			Status:      api.SessionStatus(row.Status),
			StatusSince: statusSince,
			SeenAt:      seenAt,
		})
	}
	return out, nil
}

// LatestFinishes returns each session's newest completed turn. Sessions that
// have never completed one are absent.
func (s *sqlSessions) LatestFinishes(ctx context.Context) (map[string]time.Time, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.core.queries.ListLatestFinishedTurns(ctx)
	if err != nil {
		return nil, fmt.Errorf("list latest finished turns: %w", err)
	}
	out := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		at, err := db.ParseTime(row.LastFinishedAt)
		if err != nil {
			return nil, fmt.Errorf("latest finished turn for %s: %w", row.SessionID, err)
		}
		out[row.SessionID] = at
	}
	return out, nil
}
