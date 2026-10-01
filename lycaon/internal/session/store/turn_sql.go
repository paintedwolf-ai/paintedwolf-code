package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/db"
)

// BeginTurn creates a turn or adds an attempt to a recoverable turn.
func (s *SQL) BeginTurn(ctx context.Context, in TurnStart) (TurnExecution, error) {
	if s == nil || s.db == nil {
		return TurnExecution{}, fmt.Errorf("turn store unavailable")
	}
	in.ID = strings.TrimSpace(in.ID)
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	in.SessionID = strings.TrimSpace(in.SessionID)
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	if in.SessionID == "" || in.ProjectID == "" || !validTurnOrigin(in.Origin) {
		return TurnExecution{}, fmt.Errorf("turn session, project, and origin are required")
	}
	now := time.Now().UTC()
	attemptID := uuid.NewString()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TurnExecution{}, fmt.Errorf("begin turn: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if workerJobID := strings.TrimSpace(in.WorkerJobID); workerJobID != "" {
		recoveredID, recoverErr := q.FindRecoverableTurnByWorkerJob(ctx, db.FindRecoverableTurnByWorkerJobParams{
			WorkerJobID: workerJobID,
			Origin:      string(in.Origin),
		})
		switch {
		case recoverErr == nil:
			return s.resumeTurnTx(ctx, tx, recoveredID, in, attemptID, now)
		case !db.IsNoRows(recoverErr):
			return TurnExecution{}, recoverErr
		}
	}
	if len(in.SubmissionIDs) > 0 {
		submissionID := strings.TrimSpace(in.SubmissionIDs[0])
		if submissionID != "" {
			recoveredID, recoverErr := q.FindRecoveringTurnBySubmission(ctx, submissionID)
			switch {
			case recoverErr == nil:
				return s.resumeTurnTx(ctx, tx, recoveredID, in, attemptID, now)
			case !db.IsNoRows(recoverErr):
				return TurnExecution{}, recoverErr
			}
		}
	}
	if err := q.InsertTurn(ctx, db.InsertTurnParams{
		ID: in.ID, SessionID: in.SessionID, ProjectID: in.ProjectID,
		Origin: string(in.Origin), InputJson: in.InputJSON,
		ActiveAttemptID: db.NullString(attemptID), CreatedAt: db.FormatTime(now), UpdatedAt: db.FormatTime(now),
		ProgressedAt: db.FormatTime(now),
	}); err != nil {
		return TurnExecution{}, fmt.Errorf("insert turn: %w", err)
	}
	if err := q.InsertTurnAttempt(ctx, db.InsertTurnAttemptParams{
		ID: attemptID, TurnID: in.ID, Attempt: 1,
		StartedAt: db.FormatTime(now), UpdatedAt: db.FormatTime(now),
	}); err != nil {
		return TurnExecution{}, fmt.Errorf("insert turn attempt: %w", err)
	}
	for position, submissionID := range in.SubmissionIDs {
		submissionID = strings.TrimSpace(submissionID)
		if submissionID == "" {
			continue
		}
		if err := q.LinkTurnSubmission(ctx, db.LinkTurnSubmissionParams{
			TurnID: in.ID, SubmissionID: submissionID, Position: int64(position),
		}); err != nil {
			return TurnExecution{}, fmt.Errorf("link turn submission: %w", err)
		}
	}
	if workerJobID := strings.TrimSpace(in.WorkerJobID); workerJobID != "" {
		if err := q.LinkWorkerTurn(ctx, db.LinkWorkerTurnParams{WorkerJobID: workerJobID, TurnID: in.ID}); err != nil {
			return TurnExecution{}, fmt.Errorf("link worker turn: %w", err)
		}
	}
	if in.Origin == TurnOriginUser {
		if err := q.ClearSessionCloseoutHead(ctx, in.SessionID); err != nil {
			return TurnExecution{}, err
		}
	}
	if err := q.SetSessionTurnHead(ctx, db.SetSessionTurnHeadParams{SessionID: in.SessionID, TurnID: in.ID}); err != nil {
		return TurnExecution{}, err
	}
	if err := tx.Commit(); err != nil {
		return TurnExecution{}, fmt.Errorf("commit turn: %w", err)
	}
	return TurnExecution{
		Turn: Turn{
			ID: in.ID, SessionID: in.SessionID, ProjectID: in.ProjectID,
			Origin: in.Origin, InputJSON: in.InputJSON, Status: TurnStatusRunning,
			Revision: 1, ActiveAttemptID: attemptID, CreatedAt: now, UpdatedAt: now, ProgressedAt: now,
		},
		Attempt: TurnAttempt{
			ID: attemptID, TurnID: in.ID, Attempt: 1, Status: TurnStatusRunning,
			Phase: TurnPhasePreparing, CheckpointJSON: "{}", StartedAt: now, UpdatedAt: now,
		},
	}, nil
}

func (s *SQL) resumeTurnTx(
	ctx context.Context,
	tx *sql.Tx,
	turnID string,
	in TurnStart,
	attemptID string,
	now time.Time,
) (TurnExecution, error) {
	q := s.queries.WithTx(tx)
	turnRow, err := q.GetTurn(ctx, turnID)
	if err != nil {
		return TurnExecution{}, err
	}
	turn, err := turnFromDB(turnRow)
	if err != nil {
		return TurnExecution{}, err
	}
	if turn.SessionID != in.SessionID || turn.ProjectID != in.ProjectID {
		return TurnExecution{}, fmt.Errorf("recovering turn identity mismatch: %s", turnID)
	}
	nextAttempt, err := q.NextTurnAttempt(ctx, turnID)
	if err != nil {
		return TurnExecution{}, err
	}
	if err := q.InsertTurnAttempt(ctx, db.InsertTurnAttemptParams{
		ID: attemptID, TurnID: turnID, Attempt: nextAttempt,
		StartedAt: db.FormatTime(now), UpdatedAt: db.FormatTime(now),
	}); err != nil {
		return TurnExecution{}, err
	}
	rows, err := q.ResumeTurn(ctx, db.ResumeTurnParams{
		ActiveAttemptID: db.NullString(attemptID), UpdatedAt: db.FormatTime(now), ID: turnID,
	})
	if err != nil {
		return TurnExecution{}, err
	}
	if rows != 1 {
		return TurnExecution{}, fmt.Errorf("turn resume claim lost: %s", turnID)
	}
	turn.Status = TurnStatusRunning
	turn.Revision++
	turn.ActiveAttemptID = attemptID
	turn.Error = ""
	turn.UpdatedAt = now
	turn.CompletedAt = nil
	attempt := TurnAttempt{
		ID: attemptID, TurnID: turnID, Attempt: int(nextAttempt), Status: TurnStatusRunning,
		Phase: TurnPhasePreparing, CheckpointJSON: "{}", StartedAt: now, UpdatedAt: now,
	}
	if err := q.SetSessionTurnHead(ctx, db.SetSessionTurnHeadParams{SessionID: in.SessionID, TurnID: turnID}); err != nil {
		return TurnExecution{}, err
	}
	if err := tx.Commit(); err != nil {
		return TurnExecution{}, err
	}
	return TurnExecution{Turn: turn, Attempt: attempt}, nil
}

// CheckpointTurn advances one active attempt at a restart-safe boundary.
func (s *SQL) CheckpointTurn(ctx context.Context, turnID, attemptID string, phase TurnPhase, checkpointJSON string) error {
	if !validTurnPhase(phase) || phase == TurnPhaseComplete {
		return fmt.Errorf("invalid active turn phase %q", phase)
	}
	if strings.TrimSpace(checkpointJSON) == "" {
		checkpointJSON = "{}"
	}
	now := db.FormatTime(time.Now().UTC())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if err := checkpointTurnTx(ctx, q, turnID, attemptID, phase, checkpointJSON, now); err != nil {
		return err
	}
	return tx.Commit()
}

func checkpointTurnTx(ctx context.Context, q *db.Queries, turnID, attemptID string, phase TurnPhase, checkpointJSON, now string) error {
	rows, err := q.CheckpointTurnAttempt(ctx, db.CheckpointTurnAttemptParams{
		Phase: string(phase), CheckpointJson: checkpointJSON, UpdatedAt: now,
		ID: attemptID, TurnID: turnID,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("turn attempt claim lost: %s", attemptID)
	}
	rows, err = q.TouchCheckpointedTurn(ctx, db.TouchCheckpointedTurnParams{
		UpdatedAt: now, ProgressedAt: now, ID: turnID, ActiveAttemptID: db.NullString(attemptID),
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("turn head claim lost: %s", turnID)
	}
	return nil
}

// ProjectLiveModelOutput replaces the bounded stream projection.
func (s *SQL) ProjectLiveModelOutput(ctx context.Context, out LiveModelOutput) error {
	if out.ID == "" || out.TurnAttemptID == "" || out.SessionID == "" || out.MessageID == "" || out.Iteration <= 0 {
		return fmt.Errorf("live model output identity required")
	}
	now := time.Now().UTC()
	if out.CreatedAt.IsZero() {
		out.CreatedAt = now
	}
	return s.queries.UpsertLiveModelOutput(ctx, db.UpsertLiveModelOutputParams{
		ID: out.ID, TurnAttemptID: out.TurnAttemptID, SessionID: out.SessionID,
		Iteration: int64(out.Iteration), MessageID: out.MessageID, Content: out.Content,
		ToolCallsJson: db.NullString(out.ToolCallsJSON), ReasoningJson: db.NullString(out.ReasoningJSON),
		CreatedAt: db.FormatTime(out.CreatedAt), UpdatedAt: db.FormatTime(now),
	})
}

// SettleModelOutput copies one live output into immutable storage exactly once.
func (s *SQL) SettleModelOutput(ctx context.Context, out ModelOutput) (ModelOutput, error) {
	if out.ID == "" || out.TurnAttemptID == "" || out.SessionID == "" || out.MessageID == "" || out.Iteration <= 0 {
		return ModelOutput{}, fmt.Errorf("model output identity required")
	}
	now := time.Now().UTC()
	if out.CreatedAt.IsZero() {
		out.CreatedAt = now
	}
	if out.SettledAt.IsZero() {
		out.SettledAt = now
	}
	release := bloblifecycle.AcquirePublication(s.dataDir)
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ModelOutput{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if _, err := q.GetModelOutput(ctx, out.ID); db.IsNoRows(err) {
		active, err := q.SessionHasActiveTurnAttempt(ctx, db.SessionHasActiveTurnAttemptParams{
			SessionID: out.SessionID, AttemptID: db.NullString(out.TurnAttemptID),
		})
		if err != nil {
			return ModelOutput{}, err
		}
		if active == 0 {
			return ModelOutput{}, fmt.Errorf("model output attempt claim lost")
		}
	} else if err != nil {
		return ModelOutput{}, err
	}
	projectID, err := q.GetSessionProjectID(ctx, out.SessionID)
	if err != nil {
		return ModelOutput{}, fmt.Errorf("resolve model output project: %w", err)
	}
	sha, err := s.writeModelOutputBlob(ctx, q, projectID, out.Content, out.ToolCallsJSON, out.ReasoningJSON)
	if err != nil {
		return ModelOutput{}, err
	}
	if err := q.InsertModelOutput(ctx, db.InsertModelOutputParams{
		ID: out.ID, TurnAttemptID: out.TurnAttemptID, SessionID: out.SessionID, ProjectID: projectID,
		Iteration: int64(out.Iteration), MessageID: out.MessageID, ProviderID: out.ProviderID,
		Model: out.Model, ContentBlobSha256: sha, FinishReason: out.FinishReason, Scripted: boolInt(out.Scripted),
		CreatedAt: db.FormatTime(out.CreatedAt), SettledAt: db.FormatTime(out.SettledAt),
	}); err != nil {
		return ModelOutput{}, err
	}
	committedRow, err := q.GetModelOutput(ctx, out.ID)
	if err != nil {
		return ModelOutput{}, err
	}
	if !sameSettledModelOutputRow(committedRow, out, sha) {
		return ModelOutput{}, fmt.Errorf("model output id %s reused with different content or identity", out.ID)
	}
	committed := out
	committed.CreatedAt, err = db.ParseTime(committedRow.CreatedAt)
	if err != nil {
		return ModelOutput{}, err
	}
	committed.SettledAt, err = db.ParseTime(committedRow.SettledAt)
	if err != nil {
		return ModelOutput{}, err
	}
	if err := q.DeleteLiveModelOutput(ctx, out.ID); err != nil {
		return ModelOutput{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModelOutput{}, err
	}
	return committed, nil
}

func (s *SQL) ListPendingModelOutputProjections(ctx context.Context) ([]PendingModelOutputProjection, error) {
	rows, err := s.queries.ListPendingModelOutputs(ctx)
	if err != nil {
		return nil, err
	}
	var out []PendingModelOutputProjection
	for _, row := range rows {
		output, convertErr := s.modelOutputFromDB(row)
		if convertErr != nil {
			return nil, convertErr
		}
		workerJobID, workerErr := s.queries.GetWorkerJobForTurnAttempt(ctx, output.TurnAttemptID)
		if workerErr != nil && !db.IsNoRows(workerErr) {
			return nil, workerErr
		}
		out = append(out, PendingModelOutputProjection{Output: output, WorkerJobID: workerJobID})
	}
	return out, nil
}

func (s *SQL) MarkModelOutputProjected(ctx context.Context, outputID string) error {
	if strings.TrimSpace(outputID) == "" {
		return fmt.Errorf("model output projection identity required")
	}
	rows, err := s.queries.MarkModelOutputProjected(ctx, db.MarkModelOutputProjectedParams{
		ProjectedAt: db.FormatTime(time.Now().UTC()), ID: outputID,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("model output projection not found: %s", outputID)
	}
	return nil
}

// FinishTurn atomically settles the active attempt and turn head.
func (s *SQL) FinishTurn(ctx context.Context, turnID, attemptID string, status TurnStatus, finalOutputID, resultJSON, failure string) (Turn, error) {
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
func (s *SQL) RecoverTurns(ctx context.Context) ([]Turn, error) {
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
func (s *SQL) RecoverTurnsForSession(ctx context.Context, sessionID string) ([]Turn, error) {
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

func (s *SQL) modelOutputFromDB(row db.ModelOutputs) (ModelOutput, error) {
	content, toolCallsJSON, reasoningJSON, err := s.readModelOutputBlob(row.ProjectID, row.ContentBlobSha256)
	if err != nil {
		return ModelOutput{}, err
	}
	out := ModelOutput{
		ID: row.ID, TurnAttemptID: row.TurnAttemptID, SessionID: row.SessionID,
		Iteration: int(row.Iteration), MessageID: row.MessageID, ProviderID: row.ProviderID,
		Model: row.Model, Content: content, ToolCallsJSON: toolCallsJSON,
		ReasoningJSON: reasoningJSON, FinishReason: row.FinishReason, Scripted: row.Scripted != 0,
	}
	out.CreatedAt, err = db.ParseTime(row.CreatedAt)
	if err != nil {
		return ModelOutput{}, err
	}
	out.SettledAt, err = db.ParseTime(row.SettledAt)
	return out, err
}

// sameSettledModelOutputRow compares by content digest, which covers text, tool
// calls, and reasoning without reading blobs.
func sameSettledModelOutputRow(committed db.ModelOutputs, proposed ModelOutput, proposedSha string) bool {
	return committed.ID == proposed.ID &&
		committed.TurnAttemptID == proposed.TurnAttemptID &&
		committed.SessionID == proposed.SessionID &&
		committed.Iteration == int64(proposed.Iteration) &&
		committed.MessageID == proposed.MessageID &&
		committed.ProviderID == proposed.ProviderID &&
		committed.Model == proposed.Model &&
		committed.ContentBlobSha256 == proposedSha &&
		committed.FinishReason == proposed.FinishReason && (committed.Scripted != 0) == proposed.Scripted
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
