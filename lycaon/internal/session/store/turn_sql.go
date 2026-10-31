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
func (t *sqlTurns) BeginTurn(ctx context.Context, in TurnStart) (TurnExecution, error) {
	if t == nil {
		return TurnExecution{}, fmt.Errorf("turn store unavailable")
	}
	return beginTurnSQL(ctx, t.core, in)
}

func beginTurnSQL(ctx context.Context, s *sqlCore, in TurnStart) (TurnExecution, error) {
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
			return resumeTurnTx(ctx, s, tx, recoveredID, in, attemptID, now)
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
				return resumeTurnTx(ctx, s, tx, recoveredID, in, attemptID, now)
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

func resumeTurnTx(
	ctx context.Context,
	s *sqlCore,
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
func (t *sqlTurns) CheckpointTurn(ctx context.Context, turnID, attemptID string, phase TurnPhase, checkpointJSON string) error {
	s := t.core
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

// ProjectLiveModelOutput replaces the bounded stream projection.
func (t *sqlTurns) ProjectLiveModelOutput(ctx context.Context, out LiveModelOutput) error {
	s := t.core
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
func (t *sqlTurns) SettleModelOutput(ctx context.Context, out ModelOutput) (ModelOutput, error) {
	return settleModelOutputSQL(ctx, t.core, out)
}

func settleModelOutputSQL(ctx context.Context, s *sqlCore, out ModelOutput) (ModelOutput, error) {
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
	sha, err := writeModelOutputBlob(ctx, s, q, projectID, out.Content, out.ToolCallsJSON, out.ReasoningJSON)
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
