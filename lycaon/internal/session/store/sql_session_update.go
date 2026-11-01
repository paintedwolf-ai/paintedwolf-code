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
func (s *sqlSessions) SetPostureTx(ctx context.Context, tx *sql.Tx, sessionID string, posture api.SessionPosture) error {
	if tx == nil {
		return fmt.Errorf("posture transaction required")
	}
	_, err := s.core.queries.WithTx(tx).SetSessionPosture(ctx, db.SetSessionPostureParams{
		Posture: string(posture), UpdatedAt: db.FormatTime(time.Now().UTC()), ID: sessionID,
	})
	return err
}

// UpdateSession mutates session fields.
func (s *sqlSessions) UpdateSession(ctx context.Context, id string, fn func(*api.Session)) error {
	return updateSession(ctx, s.core, id, fn, nil, nil)
}

// UpdateSessionWith commits related state with the session update.
func (s *sqlSessions) UpdateSessionWith(ctx context.Context, id string, fn func(*api.Session), mutate func(*sql.Tx) error) error {
	return updateSession(ctx, s.core, id, fn, nil, mutate)
}

// SetSessionStatusEvent commits status and its observer notice together.
func (s *sqlSessions) SetSessionStatusEvent(
	ctx context.Context,
	id string,
	status api.SessionStatus,
	hostError *api.SessionHostError,
) error {
	return updateSession(ctx, s.core, id, func(sess *api.Session) {
		sess.Status = status
	}, hostError, nil)
}

func updateSession(
	ctx context.Context,
	s *sqlCore,
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
	sess, err := getSessionSQL(ctx, s, id)
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
	if err := enqueueSessionEventWithHostError(ctx, s, tx, sess, api.SessionEventActionUpdated, hostError); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.outbox.Notify()
	return nil
}

// UpdateTitleIfUnset reports whether it changed the title.
func (s *sqlSessions) UpdateTitleIfUnset(ctx context.Context, id, title string) (bool, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	core := s.core
	unlock := core.lockMutation(id)
	defer unlock()
	tx, err := core.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := core.queries.WithTx(tx)
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
	if err := enqueueSessionEvent(ctx, core, tx, sess, api.SessionEventActionUpdated); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	core.outbox.Notify()
	return true, nil
}

// SetSessionStatus updates session status.
func (s *sqlSessions) SetSessionStatus(ctx context.Context, id string, status api.SessionStatus) error {
	return s.UpdateSession(ctx, id, func(sess *api.Session) {
		sess.Status = status
	})
}

// UpdateSession mutates session fields under store lock.
func (s *memorySessions) UpdateSession(ctx context.Context, id string, fn func(*api.Session)) error {
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
	updated := *sess
	fn(&updated)
	if !api.CanTransitionSessionStatus(sess.Status, updated.Status) {
		return &SessionStatusTransitionError{From: sess.Status, To: updated.Status}
	}
	// Pins and activity have their own writers; archiving or moving project clears the pin.
	updated.ActivityAt = sess.ActivityAt
	updated.PinRank = sess.PinRank
	if updated.ProjectID != sess.ProjectID || updated.ArchivedAt != nil {
		updated.PinRank = nil
	}
	updated.UpdatedAt = time.Now().UTC()
	if updated.Status != sess.Status {
		core.statusChangedAt[id] = updated.UpdatedAt
	}
	*sess = updated
	return nil
}

// UpdateTitleIfUnset sets title when still blank; reports whether a row was updated.
func (s *memorySessions) UpdateTitleIfUnset(ctx context.Context, id, title string) (bool, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	core := s.core
	core.mu.Lock()
	defer core.mu.Unlock()
	sess, ok := core.sessions[id]
	if !ok {
		return false, ErrSessionNotFound
	}
	if strings.TrimSpace(sess.Title) != "" {
		return false, nil
	}
	sess.Title = title
	sess.UpdatedAt = time.Now().UTC()
	return true, nil
}

// SetSessionStatusEvent updates status; the memory store publishes no events, so
// the host error is dropped.
func (s *memorySessions) SetSessionStatusEvent(
	ctx context.Context,
	id string,
	status api.SessionStatus,
	_ *api.SessionHostError,
) error {
	return s.SetSessionStatus(ctx, id, status)
}

// SetSessionStatus updates session status and updated_at.
func (s *memorySessions) SetSessionStatus(ctx context.Context, id string, status api.SessionStatus) error {
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
	if !api.CanTransitionSessionStatus(sess.Status, status) {
		return &SessionStatusTransitionError{From: sess.Status, To: status}
	}
	sess.UpdatedAt = time.Now().UTC()
	if status != sess.Status {
		core.statusChangedAt[id] = sess.UpdatedAt
	}
	sess.Status = status
	return nil
}

// sessionFromRow maps the shared session query shape.
func sessionFromRow(r db.GetSessionRow) (*api.Session, error) {
	sess := api.Session{
		ID:                   r.ID,
		Posture:              api.SessionPosture(r.Posture),
		CompactionGeneration: int(r.CompactionGeneration),
		Status:               api.SessionStatus(r.Status),
		Title:                db.StringFromNull(r.Title),
		ProjectID:            r.ProjectID,
		OwnerPersonID:        r.OwnerPersonID,
		WorkspaceRootID:      db.StringFromNull(r.WorkspaceRootID),
		AgentType:            db.StringFromNull(r.AgentType),
		ProviderID:           db.StringFromNull(r.ProviderID),
		Model:                db.StringFromNull(r.Model),
		ParentSessionID:      db.StringFromNull(r.ParentSessionID),
	}
	if r.MaxToolLoops.Valid && r.MaxToolLoops.Int64 > 0 {
		sess.MaxToolLoops = int(r.MaxToolLoops.Int64)
	}
	var err error
	sess.CreatedAt, err = db.ParseTime(r.CreatedAt)
	if err != nil {
		return nil, err
	}
	sess.ActivityAt, err = db.ParseTime(r.ActivityAt)
	if err != nil {
		return nil, err
	}
	sess.UpdatedAt, err = db.ParseTime(r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	sess.ArchivedAt, err = db.TimePtrFromNull(r.ArchivedAt)
	if err != nil {
		return nil, err
	}
	if r.PinRank.Valid {
		rank := int(r.PinRank.Int64)
		sess.PinRank = &rank
	}
	sess.SeenAt, err = db.TimePtrFromNull(r.SeenAt)
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
