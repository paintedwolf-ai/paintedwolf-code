package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetPostureTx updates posture in the caller's transaction.
func (s *SQL) SetPostureTx(ctx context.Context, tx *sql.Tx, sessionID string, posture api.SessionPosture) error {
	if tx == nil {
		return fmt.Errorf("posture transaction required")
	}
	_, err := s.queries.WithTx(tx).SetSessionPosture(ctx, db.SetSessionPostureParams{
		Posture: string(posture), UpdatedAt: db.FormatTime(time.Now().UTC()), ID: sessionID,
	})
	return err
}

// UpdateSession mutates session fields.
func (s *SQL) UpdateSession(ctx context.Context, id string, fn func(*api.Session)) error {
	return s.updateSession(ctx, id, fn, nil, nil)
}

// UpdateSessionWith commits related state with the session update.
func (s *SQL) UpdateSessionWith(ctx context.Context, id string, fn func(*api.Session), mutate func(*sql.Tx) error) error {
	return s.updateSession(ctx, id, fn, nil, mutate)
}

// SetSessionStatusEvent commits status and its observer notice together.
func (s *SQL) SetSessionStatusEvent(
	ctx context.Context,
	id string,
	status api.SessionStatus,
	hostError *api.SessionHostError,
) error {
	return s.updateSession(ctx, id, func(sess *api.Session) {
		sess.Status = status
	}, hostError, nil)
}

func (s *SQL) updateSession(
	ctx context.Context,
	id string,
	fn func(*api.Session),
	hostError *api.SessionHostError,
	mutate func(*sql.Tx) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock := s.lockMutation(id)
	defer unlock()
	sess, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	previousStatus := sess.Status
	fn(sess)
	if !api.CanTransitionSessionStatus(previousStatus, sess.Status) {
		return &SessionStatusTransitionError{From: previousStatus, To: sess.Status}
	}
	sess.UpdatedAt = time.Now().UTC()
	maxLoops := sql.NullInt64{}
	if sess.MaxToolLoops > 0 {
		maxLoops = sql.NullInt64{Int64: int64(sess.MaxToolLoops), Valid: true}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)
	if err := qtx.UpdateSession(ctx, db.UpdateSessionParams{
		ProjectID:            sess.ProjectID,
		Posture:              string(sess.Posture),
		AgentType:            db.NullString(sess.AgentType),
		ProviderID:           db.NullString(sess.ProviderID),
		Model:                db.NullString(sess.Model),
		MaxToolLoops:         maxLoops,
		Title:                db.NullString(sess.Title),
		Status:               string(sess.Status),
		CompactionGeneration: int64(sess.CompactionGeneration),
		ArchivedAt:           db.NullTimePtr(sess.ArchivedAt),
		SeenAt:               db.NullTimePtr(sess.SeenAt),
		UpdatedAt:            db.FormatTime(sess.UpdatedAt),
		ID:                   id,
	}); err != nil {
		return err
	}
	if mutate != nil {
		if err := mutate(tx); err != nil {
			return err
		}
	}
	if err := s.enqueueSessionEventWithHostError(ctx, tx, sess, api.SessionEventActionUpdated, hostError); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.outbox.Notify()
	return nil
}

// UpdateTitleIfUnset reports whether it changed the title.
func (s *SQL) UpdateTitleIfUnset(ctx context.Context, id, title string) (bool, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	unlock := s.lockMutation(id)
	defer unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)
	n, err := qtx.UpdateSessionTitleIfUnset(ctx, db.UpdateSessionTitleIfUnsetParams{
		Title:     db.NullString(title),
		UpdatedAt: db.FormatTime(time.Now().UTC()),
		ID:        id,
	})
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	row, err := qtx.GetSession(ctx, id)
	if err != nil {
		return false, err
	}
	sess, err := sessionFromRow(row)
	if err != nil {
		return false, err
	}
	if err := s.enqueueSessionEvent(ctx, tx, sess, api.SessionEventActionUpdated); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	s.outbox.Notify()
	return true, nil
}

// SetSessionStatus updates session status.
func (s *SQL) SetSessionStatus(ctx context.Context, id string, status api.SessionStatus) error {
	return s.UpdateSession(ctx, id, func(sess *api.Session) {
		sess.Status = status
	})
}
