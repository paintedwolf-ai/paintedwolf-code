package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/db"
)

var ErrModelResponseLimit = errors.New("coordinator response allowance exhausted")

type ModelLimit struct {
	SessionID          string `json:"session_id"`
	Limit              int    `json:"limit"`
	Completed          int    `json:"completed"`
	ExhaustedAttemptID string `json:"exhausted_attempt_id,omitempty"`
}

func (s *SQL) ConfigureModelLimit(ctx context.Context, sessionID string, limit int) error {
	if limit < 1 {
		return fmt.Errorf("response allowance must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	existing, err := q.GetSessionModelLimit(ctx, sessionID)
	if err == nil {
		if existing.ResponseLimit != int64(limit) {
			return fmt.Errorf("response allowance identity changed")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	started, err := q.SessionHasTurns(ctx, sessionID)
	if err != nil {
		return err
	}
	if started != 0 {
		return fmt.Errorf("configure response allowance before execution")
	}
	if err := q.InstallSessionModelLimit(ctx, db.InstallSessionModelLimitParams{SessionID: sessionID, ResponseLimit: int64(limit)}); err != nil {
		return err
	}
	return tx.Commit()
}

// AdmitModelResponse counts settled outputs within the serialized session lane.
func (s *SQL) AdmitModelResponse(ctx context.Context, sessionID, attemptID string) error {
	if _, err := s.queries.GetSessionModelLimit(ctx, sessionID); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	limit, err := q.GetSessionModelLimit(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	active, err := q.SessionHasActiveTurnAttempt(ctx, db.SessionHasActiveTurnAttemptParams{
		SessionID: sessionID, AttemptID: db.NullString(attemptID),
	})
	if err != nil {
		return err
	}
	if active == 0 {
		return fmt.Errorf("model response attempt claim lost")
	}
	if limit.ExhaustedAttemptID.Valid {
		return ErrModelResponseLimit
	}
	if limit.Completed < limit.ResponseLimit {
		return nil
	}
	if err := q.ExhaustSessionModelLimit(ctx, db.ExhaustSessionModelLimitParams{SessionID: sessionID, ExhaustedAttemptID: db.NullString(attemptID)}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return ErrModelResponseLimit
}

func (s *Memory) ConfigureModelLimit(_ context.Context, sessionID string, limit int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit < 1 || s.sessions[sessionID] == nil {
		return fmt.Errorf("invalid response allowance")
	}
	if old, ok := s.modelLimits[sessionID]; ok {
		if old.Limit != limit {
			return fmt.Errorf("response allowance identity changed")
		}
		return nil
	}
	for _, turn := range s.turns {
		if turn.SessionID == sessionID {
			return fmt.Errorf("configure response allowance before execution")
		}
	}
	if s.modelLimits == nil {
		s.modelLimits = map[string]ModelLimit{}
	}
	s.modelLimits[sessionID] = ModelLimit{SessionID: sessionID, Limit: limit}
	return nil
}

func (s *Memory) AdmitModelResponse(_ context.Context, sessionID, attemptID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit, ok := s.modelLimits[sessionID]
	if !ok {
		return nil
	}
	active := false
	for _, turn := range s.turns {
		active = active || (turn.SessionID == sessionID && turn.ActiveAttemptID == attemptID && turn.Status == TurnStatusRunning)
	}
	if !active {
		return fmt.Errorf("model response attempt claim lost")
	}
	count := 0
	for _, output := range s.modelOutputs {
		if output.SessionID == sessionID && !output.Scripted {
			count++
		}
	}
	if limit.ExhaustedAttemptID != "" {
		return ErrModelResponseLimit
	}
	if count < limit.Limit {
		return nil
	}
	limit.ExhaustedAttemptID = attemptID
	s.modelLimits[sessionID] = limit
	return ErrModelResponseLimit
}
