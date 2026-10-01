package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

// AppendMessages appends messages to a session.
func (s *SQL) AppendMessages(ctx context.Context, id string, msgs ...api.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock := s.lockMutation(id)
	defer unlock()
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.AppendMessagesTx(ctx, tx, id, msgs...); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.outbox.Notify()
	return nil
}

// AppendMessagesWith commits related state with the transcript rows.
func (s *SQL) AppendMessagesWith(ctx context.Context, id string, mutate func(*sql.Tx) error, msgs ...api.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if mutate == nil {
		return fmt.Errorf("message transaction mutation required")
	}
	unlock := s.lockMutation(id)
	defer unlock()
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := mutate(tx); err != nil {
		return err
	}
	if err := s.AppendMessagesTx(ctx, tx, id, msgs...); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if s.outbox != nil {
		s.outbox.Notify()
	}
	return nil
}

// AppendMessagesTx appends transcript rows in the caller's transaction.
func (s *SQL) AppendMessagesTx(ctx context.Context, tx *sql.Tx, id string, msgs ...api.Message) error {
	if tx == nil {
		return fmt.Errorf("message append transaction required")
	}
	qtx := s.queries.WithTx(tx)
	projectID, err := qtx.GetSessionProjectID(ctx, id)
	if err != nil {
		return err
	}
	if err := rejectDuplicateAppendIDs(msgs); err != nil {
		return err
	}
	// Screen in place so inserted and published rows match.
	screenMessagesForStore(messageScreenContext(ctx, projectID, id), msgs)
	now := time.Now().UTC()
	for i := range msgs {
		msgs[i] = api.NormalizeMessageProvenance(msgs[i])
		stamped, err := applyCheckpointDecisionStampTx(ctx, tx, id, &msgs[i])
		if err != nil {
			return err
		}
		if err := requireWorkflowRunStamp(msgs[i]); err != nil {
			return err
		}
		if msgs[i].ID == "" {
			msgs[i].ID = uuid.NewString()
		}
		if msgs[i].CreatedAt.IsZero() {
			msgs[i].CreatedAt = now
		}
		seq, err := nextTranscriptSeq(ctx, qtx, id)
		if err != nil {
			return err
		}
		msgs[i].Seq = seq
		ord, err := nextTranscriptOrd(ctx, qtx, id)
		if err != nil {
			return err
		}
		msgs[i].Ord = ord
		resourceKind, resourceID := sessionEntryResource(msgs[i])
		if err := qtx.InsertSessionEntry(ctx, db.InsertSessionEntryParams{
			ID: msgs[i].ID, SessionID: id, Ord: ord,
			ResourceKind: resourceKind, ResourceID: resourceID,
			CreatedAt: db.FormatTime(msgs[i].CreatedAt),
		}); err != nil {
			if db.IsUniqueConstraint(err) {
				return duplicateMessageID(msgs[i].ID)
			}
			return err
		}
		if err := insertMessage(ctx, qtx, id, msgs[i]); err != nil {
			if db.IsUniqueConstraint(err) {
				return duplicateMessageID(msgs[i].ID)
			}
			return err
		}
		for _, part := range msgs[i].ContentParts {
			blobID := strings.TrimSpace(part.BlobID)
			if blobID == "" {
				continue
			}
			if err := qtx.InsertMessageAttachmentRef(ctx, db.InsertMessageAttachmentRefParams{
				MessageID: msgs[i].ID, ProjectID: projectID, BlobID: blobID,
			}); err != nil {
				return err
			}
		}
		if err := writeMessageSpillRefs(ctx, qtx, projectID, msgs[i]); err != nil {
			return err
		}
		if err := qtx.DeletePromptAttachmentAdmissions(ctx, msgs[i].ID); err != nil {
			return err
		}
		if stamped {
			if err := deleteCheckpointDecisionStampTx(ctx, tx, id, msgs[i]); err != nil {
				return err
			}
		}
		if err := search.SyncMessageWriteThrough(ctx, tx, projectID, id, msgs[i]); err != nil {
			return err
		}
		if err := visual.WriteRefsTx(ctx, qtx, visual.MessageRefs(projectID, id, msgs[i])); err != nil {
			return err
		}
		observerMessage := messageview.TranscriptMessage(msgs[i])
		if err := s.outbox.EnqueueTx(ctx, tx, api.EventTopicMessage, events.PublishKey{Project: projectID, Session: id}, api.MessageEvent{
			SessionID: id, Op: api.MessageChangeAppend, Seq: observerMessage.Seq, Message: observerMessage,
		}); err != nil {
			return err
		}
	}
	at := db.FormatTime(time.Now().UTC())
	if appendsActivity(msgs) {
		return qtx.TouchSessionActivity(ctx, db.TouchSessionActivityParams{At: at, ID: id})
	}
	return qtx.TouchSessionUpdatedAt(ctx, db.TouchSessionUpdatedAtParams{UpdatedAt: at, ID: id})
}

// UpdateMessage replaces one message.
func (s *SQL) UpdateMessage(ctx context.Context, sessionID, messageID string, msg api.Message) (api.Message, error) {
	if err := ctx.Err(); err != nil {
		return api.Message{}, err
	}
	unlock := s.lockMutation(sessionID)
	defer unlock()
	msg = api.NormalizeMessageProvenance(msg)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.Message{}, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)
	projectID, err := qtx.GetSessionProjectID(ctx, sessionID)
	if err != nil {
		return api.Message{}, err
	}
	msg = screenMessageForStore(messageScreenContext(ctx, projectID, sessionID), msg)
	// Creation metadata is immutable.
	existing, err := qtx.GetMessageOrdAndTS(ctx, db.GetMessageOrdAndTSParams{
		SessionID: sessionID,
		ID:        messageID,
	})
	if err != nil {
		return api.Message{}, err
	}
	msg.Ord = existing.Ord
	msg.WorkerID = db.StringFromNull(existing.WorkerJobID)
	msg.AuthorPersonID = db.StringFromNull(existing.AuthorPersonID)
	if preserved, parseErr := db.ParseTime(existing.Ts); parseErr == nil {
		msg.CreatedAt = preserved
	}
	carryCheckpointDecision(&msg, db.StringFromNull(existing.ToolResultJson))
	encoded, err := encodeMessageJSONFields(msg)
	if err != nil {
		return api.Message{}, err
	}
	seq, err := nextTranscriptSeq(ctx, qtx, sessionID)
	if err != nil {
		return api.Message{}, err
	}
	msg.Seq = seq
	n, err := qtx.UpdateMessage(ctx, db.UpdateMessageParams{
		Role:                    string(msg.Role),
		Content:                 msg.Content,
		Origin:                  string(msg.Origin),
		Authority:               string(msg.Authority),
		TrustTier:               string(msg.TrustTier),
		ContentPartsJson:        encoded.contentParts,
		HostSecretRedactionJson: encoded.hostSecretRedaction,
		Kind:                    string(msg.Kind),
		HostSignalID:            msg.HostSignalID,
		ToolCallsJson:           encoded.toolCalls,
		ToolResultJson:          encoded.toolResult,
		WorkerSummaryJson:       encoded.workerSummary,
		GroundingJson:           encoded.grounding,
		NavigationRefsJson:      encoded.navigationRefs,
		CompactedChunkJson:      encoded.compactedChunk,
		WorkflowFeedbackJson:    encoded.workflowFeedback,
		BlueprintJson:           encoded.blueprint,
		CompletionReportJson:    encoded.completionReport,
		ArtifactIdsJson:         encoded.artifactIDs,
		EvidenceHandlesJson:     encoded.evidenceHandles,
		CompactionCheckpoint:    int64(boolToInt(msg.CompactionCheckpoint)),
		Visibility:              messageVisibilityColumn(msg.Visibility),
		DraftVersionCount:       int64(msg.DraftVersionCount),
		DraftStatus:             string(msg.DraftStatus),
		Seq:                     seq,
		ReasoningJson:           encoded.reasoning,
		SessionID:               sessionID,
		ID:                      messageID,
	})
	if err != nil {
		return api.Message{}, err
	}
	if n == 0 {
		return api.Message{}, fmt.Errorf("%w: %s", ErrMessageNotFound, messageID)
	}
	msg.ID = messageID
	oldSpillRefs, err := replaceMessageSpillRefs(ctx, qtx, projectID, msg)
	if err != nil {
		return api.Message{}, err
	}
	if err := search.SyncMessageWriteThrough(ctx, tx, projectID, sessionID, msg); err != nil {
		return api.Message{}, err
	}
	if err := visual.ClearMessageRefsTx(ctx, qtx, messageID); err != nil {
		return api.Message{}, err
	}
	if err := visual.WriteRefsTx(ctx, qtx, visual.MessageRefs(projectID, sessionID, msg)); err != nil {
		return api.Message{}, err
	}
	if err := qtx.InvalidateMessageCompactionView(ctx, db.InvalidateMessageCompactionViewParams{
		SessionID: sessionID, MessageID: messageID,
	}); err != nil {
		return api.Message{}, err
	}
	observerMessage := messageview.TranscriptMessage(msg)
	if err := s.outbox.EnqueueTx(ctx, tx, api.EventTopicMessage, events.PublishKey{Project: projectID, Session: sessionID}, api.MessageEvent{
		SessionID: sessionID, Op: api.MessageChangePatch, Seq: observerMessage.Seq, Message: observerMessage,
	}); err != nil {
		return api.Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return api.Message{}, err
	}
	s.reclaimUnreferencedSpills(ctx, oldSpillRefs)
	s.outbox.Notify()
	return msg, nil
}

// PatchLiveProjection writes streaming prose and in-flight tool_calls without
// advancing seq or enqueuing an outbox event.
func (s *SQL) PatchLiveProjection(ctx context.Context, sessionID, messageID, content string, toolCalls []api.ToolCall) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock := s.lockMutation(sessionID)
	defer unlock()
	callsJSON, err := db.MarshalJSON(toolCalls)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)
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
