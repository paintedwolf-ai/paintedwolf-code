package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

func duplicateMessageID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrDuplicateMessageID
	}
	return fmt.Errorf("%w: %s", ErrDuplicateMessageID, id)
}

func rejectDuplicateAppendIDs(msgs []api.Message) error {
	seen := make(map[string]struct{}, len(msgs))
	for _, msg := range msgs {
		id := strings.TrimSpace(msg.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			return duplicateMessageID(id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func rejectExistingMessageIDs(existing []api.Message, msgs []api.Message) error {
	if len(existing) == 0 {
		return nil
	}
	have := make(map[string]struct{}, len(existing))
	for _, msg := range existing {
		id := strings.TrimSpace(msg.ID)
		if id == "" {
			continue
		}
		have[id] = struct{}{}
	}
	for _, msg := range msgs {
		id := strings.TrimSpace(msg.ID)
		if id == "" {
			continue
		}
		if _, ok := have[id]; ok {
			return duplicateMessageID(id)
		}
	}
	return nil
}

// appendsActivity reports whether an append shows in the chat's transcript,
// which is what advances activity_at. Hidden host rows and span bookkeeping,
// including a phase's explain note, change only the record.
func appendsActivity(msgs []api.Message) bool {
	for _, msg := range msgs {
		if !api.IsInternalTranscriptMessage(msg) && !api.IsWorkflowBoundaryMessage(msg) && !api.IsWorkflowExplainMessage(msg) {
			return true
		}
	}
	return false
}

// ErrWorkflowRunStampRequired is returned when a progress row lacks workflow_run_id.
var ErrWorkflowRunStampRequired = errors.New("workflow_run_id required for progress transcript rows")

func requireWorkflowRunStamp(msg api.Message) error {
	if !api.IsProgressUpdateMessage(msg) && !api.IsProgressCompleteMessage(msg) {
		return nil
	}
	if strings.TrimSpace(msg.WorkflowRunID) != "" {
		return nil
	}
	kind := strings.TrimSpace(string(msg.Kind))
	if kind == "" {
		kind = "progress"
	}
	return fmt.Errorf("%w (%s)", ErrWorkflowRunStampRequired, kind)
}

// PatchLiveProjection writes streaming prose and in-flight tool_calls without
// advancing seq or enqueuing an outbox event.
func (s *sqlSessionViews) PatchLiveProjection(ctx context.Context, sessionID, messageID, content string, toolCalls []api.ToolCall) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	core := s.core
	unlock := core.lockMutation(sessionID)
	defer unlock()
	callsJSON, err := db.MarshalJSON(toolCalls)
	if err != nil {
		return err
	}
	tx, err := core.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := core.queries.WithTx(tx)
	n, err := qtx.PatchLiveProjection(ctx, db.PatchLiveProjectionParams{
		Content:       content,
		ToolCallsJson: callsJSON,
		SessionID:     sessionID,
		ID:            messageID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrMessageNotFound, messageID)
	}
	if err := qtx.InvalidateMessageCompactionView(ctx, db.InvalidateMessageCompactionViewParams{
		SessionID: sessionID, MessageID: messageID,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// carryCheckpointDecision preserves a settled decision across stale row writes.
func carryCheckpointDecision(msg *api.Message, storedToolResultJSON string) {
	if msg == nil || msg.ToolResult == nil || msg.ToolResult.CheckpointDecision != nil {
		return
	}
	if strings.TrimSpace(storedToolResultJSON) == "" {
		return
	}
	var stored api.ToolResult
	if err := json.Unmarshal([]byte(storedToolResultJSON), &stored); err != nil {
		return
	}
	if stored.CheckpointDecision == nil {
		return
	}
	decision := *stored.CheckpointDecision
	msg.ToolResult.CheckpointDecision = &decision
}

func applyCheckpointDecisionStampTx(ctx context.Context, tx *sql.Tx, sessionID string, msg *api.Message) (bool, error) {
	if msg == nil || msg.Role != api.MessageRoleTool || msg.ToolResult == nil {
		return false, nil
	}
	toolCallID := strings.TrimSpace(msg.ToolResult.ToolCallID)
	if toolCallID == "" {
		return false, nil
	}
	qtx := db.New(tx)
	raw, err := qtx.GetCheckpointDecisionStamp(ctx, db.GetCheckpointDecisionStampParams{
		SessionID: sessionID, ToolCallID: toolCallID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	var decision api.CheckpointDecisionMeta
	if err := json.Unmarshal([]byte(raw), &decision); err != nil {
		return false, err
	}
	msg.ToolResult.CheckpointDecision = &decision
	return true, nil
}

func deleteCheckpointDecisionStampTx(ctx context.Context, tx *sql.Tx, sessionID string, msg api.Message) error {
	toolCallID := ""
	if msg.ToolResult != nil {
		toolCallID = strings.TrimSpace(msg.ToolResult.ToolCallID)
	}
	if toolCallID == "" {
		return nil
	}
	return db.New(tx).DeleteCheckpointDecisionStamp(ctx, db.DeleteCheckpointDecisionStampParams{
		SessionID: sessionID, ToolCallID: toolCallID,
	})
}
