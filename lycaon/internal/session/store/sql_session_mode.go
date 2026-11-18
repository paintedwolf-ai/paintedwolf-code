package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/pkg/api"
)

func (s *sqlSessions) ResolveEffectiveMode(ctx context.Context, id string) (RootAuthority, error) {
	if s == nil || s.core == nil || s.core.db == nil {
		return RootAuthority{}, ErrSessionModeUnavailable
	}
	return resolveEffectiveModeSQL(ctx, s.core.queries, id)
}

func (s *sqlSessions) ResolveEffectiveModeTx(ctx context.Context, tx *sql.Tx, id string) (RootAuthority, error) {
	if s == nil || s.core == nil || tx == nil {
		return RootAuthority{}, ErrSessionModeUnavailable
	}
	return resolveEffectiveModeSQL(ctx, s.core.queries.WithTx(tx), id)
}

func resolveEffectiveModeSQL(ctx context.Context, q *db.Queries, id string) (RootAuthority, error) {
	root, err := q.GetSessionModeAuthority(ctx, id)
	if err != nil {
		return RootAuthority{}, sessionModeUnavailable(id, err)
	}
	mode := api.SessionMode(root.Mode.String)
	if !root.RootSessionID.Valid || root.RootSessionID.String == "" || !validSessionMode(mode) ||
		!root.Started.Valid || (root.Started.Int64 != 0 && root.Started.Int64 != 1) {
		return RootAuthority{}, ErrSessionModeUnavailable
	}
	return RootAuthority{RootSessionID: root.RootSessionID.String, Mode: mode, Started: root.Started.Int64 == 1}, nil
}

// WithModeAdmission serializes preparations that depend on the pre-start mode.
func (s *sqlSessions) WithModeAdmission(ctx context.Context, id string, fn func(context.Context) error) error {
	if s == nil {
		return ErrSessionModeUnavailable
	}
	unlock, err := lockModeAdmission(ctx, s.core, id)
	if err != nil {
		return err
	}
	defer unlock()
	return fn(ctx)
}

func lockModeAdmission(ctx context.Context, s *sqlCore, id string) (func(), error) {
	if s == nil || s.db == nil {
		return nil, ErrSessionModeUnavailable
	}
	root, err := resolveEffectiveModeSQL(ctx, s.queries, id)
	if err != nil {
		return nil, err
	}
	lock := s.modeLocks.Acquire(root.RootSessionID)
	lock.Lock()
	if err := ctx.Err(); err != nil {
		lock.Unlock()
		return nil, err
	}
	return lock.Unlock, nil
}

// SetMode applies a lifecycle mode command independently of prompt execution.
func (s *sqlSessions) SetMode(ctx context.Context, id string, mode api.SessionMode) (out *api.Session, err error) {
	err = s.WithModeAdmission(ctx, id, func(ctx context.Context) error {
		out, err = s.setModeLocked(ctx, id, mode)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		_, lookupErr := s.core.queries.GetSessionProjectID(ctx, id)
		if db.IsNoRows(lookupErr) {
			return nil, ErrSessionNotFound
		}
	}
	return out, err
}

func (s *sqlSessions) setModeLocked(ctx context.Context, id string, mode api.SessionMode) (*api.Session, error) {
	core := s.core
	tx, err := core.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := core.queries.WithTx(tx)
	root, err := resolveEffectiveModeSQL(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if root.RootSessionID != id {
		return nil, ErrChildSessionMode
	}
	if _, err := root.WithModeIntent(&mode); err != nil {
		return nil, err
	}
	if err := putRootMode(ctx, core, tx, root, mode, root.Started); err != nil {
		return nil, err
	}
	session, err := sessionModeSnapshot(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	core.outbox.Notify()
	return session, nil
}

// AdmitModeInTx applies the admission latch and optional intent in the caller's transaction.
func (s *sqlSessions) AdmitModeInTx(ctx context.Context, tx *sql.Tx, id string, mode *api.SessionMode, validate func(api.SessionMode) error) error {
	if s == nil || s.core == nil || tx == nil {
		return ErrSessionModeUnavailable
	}
	return admitModeSQL(ctx, s.core, tx, id, mode, validate)
}

func admitModeSQL(ctx context.Context, s *sqlCore, tx *sql.Tx, id string, intent *api.SessionMode, validate func(api.SessionMode) error) error {
	root, err := resolveEffectiveModeSQL(ctx, s.queries.WithTx(tx), id)
	if err != nil {
		return err
	}
	if intent != nil && root.RootSessionID != id {
		return ErrChildSessionMode
	}
	mode, err := root.WithModeIntent(intent)
	if err != nil {
		return err
	}
	if validate != nil {
		if err := validate(mode); err != nil {
			return err
		}
	}
	return putRootMode(ctx, s, tx, root, mode, true)
}

func putRootMode(ctx context.Context, s *sqlCore, tx *sql.Tx, root RootAuthority, mode api.SessionMode, started bool) error {
	if root.Mode == mode && root.Started == started {
		return nil
	}
	q := s.queries.WithTx(tx)
	if err := q.SetRootSessionMode(ctx, db.SetRootSessionModeParams{
		ID: root.RootSessionID, Mode: db.NullString(string(mode)),
		Started: int64(boolToInt(started)), UpdatedAt: timelayout.Format(time.Now().UTC()),
	}); err != nil {
		return err
	}
	row, err := q.GetSession(ctx, root.RootSessionID)
	if err != nil {
		return err
	}
	sess, err := sessionFromRow(row)
	if err != nil {
		return err
	}
	return enqueueSessionEvent(ctx, s, tx, sess, api.SessionEventActionUpdated)
}

func sessionModeSnapshot(ctx context.Context, q *db.Queries, id string) (*api.Session, error) {
	row, err := q.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	session, err := sessionFromRow(row)
	if err != nil {
		return nil, err
	}
	turn, err := q.GetSessionUserTurnOrdinal(ctx, id)
	if err != nil {
		return nil, err
	}
	untrusted, err := q.HasUntrustedEvidence(ctx, id)
	if err != nil {
		return nil, err
	}
	session.CurrentTurn, session.UntrustedContent = int(turn), untrusted != 0
	return session, nil
}
