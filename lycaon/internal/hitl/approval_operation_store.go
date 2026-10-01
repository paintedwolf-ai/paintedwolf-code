package hitl

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

type preparedApprovalOperation struct {
	CheckpointID string
	SessionID    string
	Option       ApprovalOption
}

func (s *SQLStore) prepareApprovalOperation(ctx context.Context, checkpointID, sessionID string, option ApprovalOption) error {
	raw, err := json.Marshal(option)
	if err != nil {
		return err
	}
	rows, err := s.queries.PrepareApprovalOperation(ctx, db.PrepareApprovalOperationParams{
		CheckpointID: checkpointID,
		SessionID:    sessionID,
		OptionID:     option.ID,
		OptionJson:   string(raw),
		CreatedAt:    db.FormatTime(time.Now().UTC()),
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("approval operation %s is already active", checkpointID)
	}
	return nil
}

func (s *SQLStore) commitApprovalOperation(
	ctx context.Context,
	row StoredCheckpoint,
	result *DecisionResult,
	resolvedAt time.Time,
	resolution Resolution,
	seal func(*sql.Tx) error,
) (bool, error) {
	resultJSON, err := db.MarshalJSON(map[string]any{"tool": result})
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)
	checkpointRows, err := qtx.CommitApprovalCheckpoint(ctx, db.CommitApprovalCheckpointParams{
		ResultJson:         resultJSON,
		ResolvedAt:         db.NullString(db.FormatTime(resolvedAt)),
		ResolvedBy:         db.NullString(resolution.By),
		ResolvedByPersonID: db.NullString(resolution.PersonID),
		ID:                 row.ID,
		SessionID:          row.SessionID,
	})
	if err != nil {
		return false, err
	}
	if checkpointRows != 1 {
		return false, ErrCheckpointNotPending
	}
	operationRows, err := qtx.CommitApprovalOperation(ctx, db.CommitApprovalOperationParams{
		CommittedAt:  db.NullString(db.FormatTime(resolvedAt)),
		CheckpointID: row.ID,
	})
	if err != nil {
		return false, err
	}
	if operationRows != 1 {
		return false, fmt.Errorf("approval operation %s is not prepared", row.ID)
	}
	committed := row
	committed.Status = DecisionStatusApproved
	committed.Result = result
	committed.ResolvedAt = &resolvedAt
	committed.Resolution = &resolution
	if err := stageCheckpointDecisionTx(ctx, tx, committed, DecisionStatusApproved); err != nil {
		return false, err
	}
	applied, err := patchCheckpointDecisionTx(ctx, tx, committed, DecisionStatusApproved)
	if err != nil {
		return false, err
	}
	if applied {
		if err := deleteCheckpointDecisionStampTx(ctx, tx, committed); err != nil {
			return false, err
		}
	}
	if seal != nil {
		if err := seal(tx); err != nil {
			return false, err
		}
	}
	viaOutbox := s.outbox != nil
	if err := s.enqueueCheckpointTx(ctx, tx, committed); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if viaOutbox {
		s.outbox.Notify()
	}
	return viaOutbox, nil
}

func stageCheckpointDecisionTx(ctx context.Context, tx *sql.Tx, row StoredCheckpoint, status DecisionStatus) error {
	toolCallID := checkpointToolCallID(row)
	if toolCallID == "" {
		return nil
	}
	raw, err := json.Marshal(checkpointDecisionMeta(row, status))
	if err != nil {
		return err
	}
	return db.New(tx).UpsertCheckpointDecisionStamp(ctx, db.UpsertCheckpointDecisionStampParams{
		SessionID: row.SessionID, ToolCallID: toolCallID,
		DecisionJson: string(raw), CreatedAt: db.FormatTime(time.Now().UTC()),
	})
}

func deleteCheckpointDecisionStampTx(ctx context.Context, tx *sql.Tx, row StoredCheckpoint) error {
	toolCallID := checkpointToolCallID(row)
	if toolCallID == "" {
		return nil
	}
	return db.New(tx).DeleteCheckpointDecisionStamp(ctx, db.DeleteCheckpointDecisionStampParams{
		SessionID: row.SessionID, ToolCallID: toolCallID,
	})
}

func patchCheckpointDecisionTx(ctx context.Context, tx *sql.Tx, row StoredCheckpoint, status DecisionStatus) (bool, error) {
	toolCallID := checkpointToolCallID(row)
	if toolCallID == "" {
		return false, nil
	}
	qtx := db.New(tx)
	rows, err := qtx.ListSessionToolResults(ctx, row.SessionID)
	if err != nil {
		return false, err
	}
	for _, message := range rows {
		raw := message.ToolResultJson.String
		var result api.ToolResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil || result.ToolCallID != toolCallID {
			continue
		}
		decision := checkpointDecisionMeta(row, status)
		result.CheckpointDecision = &decision
		updated, err := json.Marshal(result)
		if err != nil {
			return false, err
		}
		if err := qtx.UpdateMessageToolResult(ctx, db.UpdateMessageToolResultParams{
			ToolResultJson: db.NullString(string(updated)), ID: message.ID, SessionID: row.SessionID,
		}); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (s *SQLStore) rollbackApprovalOperation(ctx context.Context, checkpointID string) error {
	return s.queries.RollbackApprovalOperation(ctx, db.RollbackApprovalOperationParams{
		RolledBackAt: db.NullString(db.FormatTime(time.Now().UTC())), CheckpointID: checkpointID,
	})
}

func (s *SQLStore) preparedApprovalOperations(ctx context.Context) ([]preparedApprovalOperation, error) {
	rows, err := s.queries.ListPreparedApprovalOperations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]preparedApprovalOperation, 0, len(rows))
	for _, row := range rows {
		op := preparedApprovalOperation{CheckpointID: row.CheckpointID, SessionID: row.SessionID}
		if err := json.Unmarshal([]byte(row.OptionJson), &op.Option); err != nil {
			return nil, fmt.Errorf("decode approval operation %s: %w", op.CheckpointID, err)
		}
		out = append(out, op)
	}
	return out, nil
}
