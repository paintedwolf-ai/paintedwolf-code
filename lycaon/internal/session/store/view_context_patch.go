package store

import (
	"context"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/pkg/api"
)

// PatchMessageViewContext records a prompt's view context once; a message
// that already carries one keeps it.
func (s *sqlTranscript) PatchMessageViewContext(ctx context.Context, sessionID, messageID string, view api.ViewContext) (api.Message, error) {
	core := s.core
	unlock := core.lockMutation(sessionID)
	defer unlock()
	msg, err := s.GetMessage(ctx, sessionID, messageID)
	if err != nil {
		return api.Message{}, err
	}
	if msg.ViewContext != nil {
		return msg, nil
	}
	msg.ViewContext = &view
	encoded, err := db.MarshalJSON(msg.ViewContext)
	if err != nil {
		return api.Message{}, err
	}
	tx, err := core.db.BeginTx(ctx, nil)
	if err != nil {
		return api.Message{}, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := core.queries.WithTx(tx)
	projectID, err := qtx.GetSessionProjectID(ctx, sessionID)
	if err != nil {
		return api.Message{}, err
	}
	if msg.Seq, err = nextTranscriptSeq(ctx, qtx, sessionID); err != nil {
		return api.Message{}, err
	}
	if err := qtx.PatchMessageViewContext(ctx, db.PatchMessageViewContextParams{
		ViewContextJson: encoded, Seq: msg.Seq, SessionID: sessionID, ID: messageID,
	}); err != nil {
		return api.Message{}, err
	}
	observer := messageview.TranscriptMessage(msg)
	if err := core.outbox.EnqueueTx(ctx, tx, api.EventTopicMessage, events.PublishKey{Project: projectID, Session: sessionID}, api.MessageEvent{SessionID: sessionID, Op: api.MessageChangePatch, Seq: msg.Seq, Message: observer}); err != nil {
		return api.Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return api.Message{}, err
	}
	core.outbox.Notify()
	return msg, nil
}

func (s *memoryTranscript) PatchMessageViewContext(ctx context.Context, sessionID, messageID string, view api.ViewContext) (api.Message, error) {
	if err := ctx.Err(); err != nil {
		return api.Message{}, err
	}
	core := s.core
	core.mu.Lock()
	defer core.mu.Unlock()
	if _, ok := core.sessions[sessionID]; !ok {
		return api.Message{}, ErrSessionNotFound
	}
	for i, msg := range core.messages[sessionID] {
		if msg.ID != messageID {
			continue
		}
		if msg.ViewContext != nil {
			return msg, nil
		}
		msg.ViewContext = &view
		msg.Seq = nextSeqLocked(core, sessionID)
		core.messages[sessionID][i] = msg
		return msg, nil
	}
	return api.Message{}, ErrMessageNotFound
}
