package store

import (
	"context"
	"encoding/json"
	"fmt"
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

// SealTurnCloseout atomically saves the closeout and its recovery checkpoint.
func (s *SQL) SealTurnCloseout(ctx context.Context, commit TurnCloseoutCommit) error {
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

func (s *Memory) SealTurnCloseout(ctx context.Context, commit TurnCloseoutCommit) error {
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
	if err := s.checkpointTurnLocked(turnID, attemptID, TurnPhaseFinalizing, commit.CheckpointJSON); err != nil {
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
