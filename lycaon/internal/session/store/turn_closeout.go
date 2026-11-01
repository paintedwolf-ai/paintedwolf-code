package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// TurnCloseout stores terminal presentation and grounding.
type TurnCloseout struct {
	MessageID     string                 `json:"message_id"`
	OutputID      string                 `json:"output_id"`
	ModelAuthored bool                   `json:"model_authored"`
	Content       string                 `json:"content"`
	Visible       bool                   `json:"visible"`
	HasToolCalls  bool                   `json:"has_tool_calls"`
	Grounding     *api.CitationGrounding `json:"grounding,omitempty"`
	ArtifactIDs   []string               `json:"artifact_ids,omitempty"`
}

func closeoutRecord(message api.Message, outputID, outputMessageID string, scripted bool) TurnCloseout {
	result := TurnCloseout{MessageID: message.ID, OutputID: outputID,
		ModelAuthored: !scripted && outputID != "" && outputMessageID == message.ID && message.Origin != api.MessageOriginHost && (message.Grounding == nil || !message.Grounding.HostAssembled),
		Visible:       message.Visibility != api.MessageVisibilityInternal, HasToolCalls: len(message.ToolCalls) > 0,
		Grounding: message.Grounding, ArtifactIDs: message.ArtifactIDs}
	if !result.HasToolCalls {
		result.Content = message.Content
	}
	return result
}

type TurnCloseoutCommit struct {
	TurnID         string
	AttemptID      string
	OutputID       string
	CheckpointJSON string
	Message        *api.Message
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

func (t *sqlTurns) ListPendingModelOutputProjections(ctx context.Context) ([]PendingModelOutputProjection, error) {
	s := t.core
	rows, err := s.queries.ListPendingModelOutputs(ctx)
	if err != nil {
		return nil, err
	}
	var out []PendingModelOutputProjection
	for _, row := range rows {
		output, convertErr := modelOutputFromDB(s, row)
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

func (t *sqlTurns) MarkModelOutputProjected(ctx context.Context, outputID string) error {
	s := t.core
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

// SealTurnCloseout atomically saves the closeout and its recovery checkpoint.
func (t *sqlTurns) SealTurnCloseout(ctx context.Context, commit TurnCloseoutCommit) error {
	return sealTurnCloseoutSQL(ctx, t.core, commit)
}

func sealTurnCloseoutSQL(ctx context.Context, s *sqlCore, commit TurnCloseoutCommit) error {
	turnID, attemptID, outputID := commit.TurnID, commit.AttemptID, commit.OutputID
	message := api.Message{}
	if commit.Message != nil {
		message = *commit.Message
	}
	if !json.Valid([]byte(commit.CheckpointJSON)) {
		return fmt.Errorf("invalid closeout checkpoint")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	turn, err := q.GetTurn(ctx, turnID)
	if err != nil {
		return err
	}
	if turn.ActiveAttemptID.String != attemptID || turn.Status != "running" {
		return fmt.Errorf("closeout attempt claim lost")
	}
	outputMessageID := ""
	scripted := false
	if outputID != "" {
		output, err := q.GetModelOutput(ctx, outputID)
		if err != nil {
			return err
		}
		if output.TurnAttemptID != attemptID {
			return fmt.Errorf("closeout output belongs to another attempt")
		}
		outputMessageID = output.MessageID
		scripted = output.Scripted != 0
	}
	record := closeoutRecord(message, outputID, outputMessageID, scripted)
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := q.InsertTurnAttemptCloseout(ctx, db.InsertTurnAttemptCloseoutParams{TurnAttemptID: attemptID, CloseoutJson: string(body)}); err != nil {
		return err
	}
	old, err := q.GetTurnAttemptCloseout(ctx, attemptID)
	if err != nil {
		return err
	}
	if old != string(body) {
		return fmt.Errorf("turn closeout identity changed")
	}
	if record.Visible && !record.HasToolCalls && record.Content != "" {
		if err := q.SetSessionCloseoutHead(ctx, db.SetSessionCloseoutHeadParams{SessionID: turn.SessionID, CloseoutAttemptID: db.NullString(attemptID)}); err != nil {
			return err
		}
	}
	if err := checkpointTurnTx(ctx, q, turnID, attemptID, TurnPhaseFinalizing, commit.CheckpointJSON, db.FormatTime(time.Now().UTC())); err != nil {
		return err
	}
	return tx.Commit()
}

func (t *memoryTurns) SealTurnCloseout(ctx context.Context, commit TurnCloseoutCommit) error {
	return sealTurnCloseoutMemory(ctx, t.core, commit)
}

func sealTurnCloseoutMemory(ctx context.Context, s *memoryCore, commit TurnCloseoutCommit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	turnID, attemptID, outputID := commit.TurnID, commit.AttemptID, commit.OutputID
	message := api.Message{}
	if commit.Message != nil {
		message = *commit.Message
	}
	if !json.Valid([]byte(commit.CheckpointJSON)) {
		return fmt.Errorf("invalid closeout checkpoint")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	turn, ok := s.turns[turnID]
	if !ok || turn.ActiveAttemptID != attemptID || turn.Status != TurnStatusRunning {
		return fmt.Errorf("closeout attempt claim lost")
	}
	outputMessageID := ""
	scripted := false
	if outputID != "" {
		output, ok := s.modelOutputs[outputID]
		if !ok || output.TurnAttemptID != attemptID {
			return fmt.Errorf("closeout output belongs to another attempt")
		}
		outputMessageID = output.MessageID
		scripted = output.Scripted
	}
	record := closeoutRecord(message, outputID, outputMessageID, scripted)
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if old, ok := s.turnCloseouts[attemptID]; ok && old != string(body) {
		return fmt.Errorf("turn closeout identity changed")
	}
	if err := checkpointTurnLocked(s, turnID, attemptID, TurnPhaseFinalizing, commit.CheckpointJSON); err != nil {
		return err
	}
	if s.turnCloseouts == nil {
		s.turnCloseouts = map[string]string{}
	}
	s.turnCloseouts[attemptID] = string(body)
	return nil
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
