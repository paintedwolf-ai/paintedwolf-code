package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// FinishTurn atomically settles the active attempt and turn head.
func (t *sqlTurns) FinishTurn(ctx context.Context, turnID, attemptID string, status TurnStatus, finalOutputID, resultJSON, failure string) (Turn, error) {
	return finishTurnSQL(ctx, t.core, turnID, attemptID, status, finalOutputID, resultJSON, failure)
}

func finishTurnSQL(ctx context.Context, s *sqlCore, turnID, attemptID string, status TurnStatus, finalOutputID, resultJSON, failure string) (Turn, error) {
	if status != TurnStatusComplete && status != TurnStatusFailed && status != TurnStatusInterrupted {
		return Turn{}, fmt.Errorf("invalid terminal turn status %q", status)
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Turn{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	finishedAt := db.FormatTime(now)
	rows, err := q.FinishTurnAttempt(ctx, db.FinishTurnAttemptParams{
		Status: string(status), Error: failure, UpdatedAt: finishedAt,
		CompletedAt: db.NullString(finishedAt), ID: attemptID, TurnID: turnID,
	})
	if err != nil {
		return Turn{}, err
	}
	if rows != 1 {
		return Turn{}, fmt.Errorf("turn attempt claim lost: %s", attemptID)
	}
	rows, err = q.FinishTurnHead(ctx, db.FinishTurnHeadParams{
		Status: string(status), FinalOutputID: finalOutputID, ResultJson: resultJSON,
		Error: failure, UpdatedAt: finishedAt, CompletedAt: db.NullString(finishedAt),
		ID: turnID, ActiveAttemptID: db.NullString(attemptID),
	})
	if err != nil {
		return Turn{}, err
	}
	if rows != 1 {
		return Turn{}, fmt.Errorf("turn head claim lost: %s", turnID)
	}
	committedRow, err := q.GetTurn(ctx, turnID)
	if err != nil {
		return Turn{}, err
	}
	committed, err := turnFromDB(committedRow)
	if err != nil {
		return Turn{}, err
	}
	if err := tx.Commit(); err != nil {
		return Turn{}, err
	}
	return committed, nil
}

// RecoverTurns fences attempts abandoned by process loss.
func (t *sqlTurns) RecoverTurns(ctx context.Context) ([]Turn, error) {
	return recoverTurnsSQL(ctx, t.core)
}

func recoverTurnsSQL(ctx context.Context, s *sqlCore) ([]Turn, error) {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	recoveredAt := db.FormatTime(now)
	timestamp := db.NullString(recoveredAt)
	if err := q.CompleteFinalizingTurns(ctx, db.CompleteFinalizingTurnsParams{
		UpdatedAt: recoveredAt, CompletedAt: timestamp,
	}); err != nil {
		return nil, err
	}
	if err := q.CompleteFinalizingTurnAttempts(ctx, db.CompleteFinalizingTurnAttemptsParams{
		UpdatedAt: recoveredAt, CompletedAt: timestamp,
	}); err != nil {
		return nil, err
	}
	if err := q.CompleteSubmissionsForRecoveredTurns(ctx, timestamp); err != nil {
		return nil, err
	}
	if err := q.InterruptRunningTurnAttempts(ctx, db.InterruptRunningTurnAttemptsParams{
		UpdatedAt: recoveredAt, CompletedAt: timestamp,
	}); err != nil {
		return nil, err
	}
	if err := q.RecoverRunningTurns(ctx, db.RecoverRunningTurnsParams{
		UpdatedAt: recoveredAt, CompletedAt: timestamp,
	}); err != nil {
		return nil, err
	}
	if err := q.DeleteLiveModelOutputs(ctx); err != nil {
		return nil, err
	}
	rows, err := q.ListRecoveringTurns(ctx)
	if err != nil {
		return nil, err
	}
	var out []Turn
	for _, row := range rows {
		turn, convertErr := turnFromDB(row)
		if convertErr != nil {
			return nil, convertErr
		}
		out = append(out, turn)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// RecoverTurnsForSession fences one session while other sessions remain active.
func (t *sqlTurns) RecoverTurnsForSession(ctx context.Context, sessionID string) ([]Turn, error) {
	return recoverTurnsForSessionSQL(ctx, t.core, sessionID)
}

func recoverTurnsForSessionSQL(ctx context.Context, s *sqlCore, sessionID string) ([]Turn, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("session id required")
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	recoveredAt := db.FormatTime(now)
	timestamp := db.NullString(recoveredAt)
	if err := q.CompleteFinalizingTurnsForSession(ctx, db.CompleteFinalizingTurnsForSessionParams{
		UpdatedAt: recoveredAt, CompletedAt: timestamp, SessionID: sessionID,
	}); err != nil {
		return nil, err
	}
	if err := q.CompleteFinalizingTurnAttemptsForSession(ctx, db.CompleteFinalizingTurnAttemptsForSessionParams{
		UpdatedAt: recoveredAt, CompletedAt: timestamp, SessionID: sessionID,
	}); err != nil {
		return nil, err
	}
	if err := q.CompleteSubmissionsForRecoveredTurnsForSession(ctx, db.CompleteSubmissionsForRecoveredTurnsForSessionParams{
		CompletedAt: timestamp, SessionID: sessionID,
	}); err != nil {
		return nil, err
	}
	if err := q.InterruptRunningTurnAttemptsForSession(ctx, db.InterruptRunningTurnAttemptsForSessionParams{
		UpdatedAt: recoveredAt, CompletedAt: timestamp, SessionID: sessionID,
	}); err != nil {
		return nil, err
	}
	if err := q.RecoverRunningTurnsForSession(ctx, db.RecoverRunningTurnsForSessionParams{
		UpdatedAt: recoveredAt, CompletedAt: timestamp, SessionID: sessionID,
	}); err != nil {
		return nil, err
	}
	if err := q.DeleteLiveModelOutputsForSession(ctx, sessionID); err != nil {
		return nil, err
	}
	rows, err := q.ListRecoveringTurnsForSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	var out []Turn
	for _, row := range rows {
		turn, convertErr := turnFromDB(row)
		if convertErr != nil {
			return nil, convertErr
		}
		out = append(out, turn)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func turnFromDB(row db.Turns) (Turn, error) {
	out := Turn{
		ID: row.ID, SessionID: row.SessionID, ProjectID: row.ProjectID,
		Origin: TurnOrigin(row.Origin), InputJSON: row.InputJson, Status: TurnStatus(row.Status),
		Revision: row.Revision, ActiveAttemptID: row.ActiveAttemptID.String,
		FinalOutputID: row.FinalOutputID.String, ResultJSON: row.ResultJson.String, Error: row.Error,
	}
	var err error
	out.CreatedAt, err = db.ParseTime(row.CreatedAt)
	if err != nil {
		return Turn{}, err
	}
	out.UpdatedAt, err = db.ParseTime(row.UpdatedAt)
	if err != nil {
		return Turn{}, err
	}
	out.ProgressedAt, err = db.ParseTime(row.ProgressedAt)
	if err != nil {
		return Turn{}, err
	}
	if row.CompletedAt.Valid {
		parsed, parseErr := db.ParseTime(row.CompletedAt.String)
		if parseErr != nil {
			return Turn{}, parseErr
		}
		out.CompletedAt = &parsed
	}
	return out, nil
}

func validTurnOrigin(origin TurnOrigin) bool {
	switch origin {
	case TurnOriginUser, TurnOriginLoopWake, TurnOriginWorker, TurnOriginWorkerCloseout, TurnOriginGroundingRetry:
		return true
	default:
		return false
	}
}

func validTurnPhase(phase TurnPhase) bool {
	switch phase {
	case TurnPhasePreparing, TurnPhaseModel, TurnPhaseTools, TurnPhaseDecision, TurnPhaseFinalizing, TurnPhaseComplete:
		return true
	default:
		return false
	}
}
