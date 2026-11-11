package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

var ErrNavigationContentChanged = errors.New("navigation message content changed")

func NavigationContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// PatchMessageNavigation changes only navigation under the message mutation lock.
func (s *sqlTranscript) PatchMessageNavigation(ctx context.Context, sessionID, messageID, contentHash string, expected, refs []api.NavigationReference) (api.Message, error) {
	core := s.core
	unlock := core.lockMutation(sessionID)
	defer unlock()
	msg, err := s.GetMessage(ctx, sessionID, messageID)
	if err != nil {
		return api.Message{}, err
	}
	if NavigationContentHash(msg.Content) != contentHash {
		return api.Message{}, ErrNavigationContentChanged
	}
	if !reflect.DeepEqual(msg.NavigationRefs, expected) || reflect.DeepEqual(msg.NavigationRefs, refs) {
		return msg, nil
	}
	msg.NavigationRefs = refs
	projectForScreen, err := core.queries.GetSessionProjectID(ctx, sessionID)
	if err != nil {
		return api.Message{}, err
	}
	msg = screenMessageForStore(messageScreenContext(ctx, projectForScreen, sessionID), msg)
	encoded, err := sourceref.EncodeMetadata(msg.NavigationRefs, msg.SourceContext)
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
	seq, err := nextTranscriptSeq(ctx, qtx, sessionID)
	if err != nil {
		return api.Message{}, err
	}
	msg.Seq = seq
	if err := qtx.PatchMessageNavigation(ctx, db.PatchMessageNavigationParams{
		NavigationRefsJson: encoded, Seq: seq, SessionID: sessionID, ID: messageID,
	}); err != nil {
		return api.Message{}, err
	}
	observer := messageview.TranscriptMessage(msg)
	if err := core.outbox.EnqueueTx(ctx, tx, api.EventTopicMessage, events.PublishKey{Project: projectID, Session: sessionID}, api.MessageEvent{SessionID: sessionID, Op: api.MessageChangePatch, Seq: seq, Message: observer}); err != nil {
		return api.Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return api.Message{}, err
	}
	core.outbox.Notify()
	return msg, nil
}

func (s *memoryTranscript) PatchMessageNavigation(ctx context.Context, sessionID, messageID, contentHash string, expected, refs []api.NavigationReference) (api.Message, error) {
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
		if NavigationContentHash(msg.Content) != contentHash {
			return api.Message{}, ErrNavigationContentChanged
		}
		if !reflect.DeepEqual(msg.NavigationRefs, expected) || reflect.DeepEqual(msg.NavigationRefs, refs) {
			return msg, nil
		}
		if _, err := sourceref.EncodeMetadata(refs, msg.SourceContext); err != nil {
			return api.Message{}, err
		}
		msg.NavigationRefs = refs
		msg = screenMessageForStore(messageScreenContext(ctx, core.sessions[sessionID].ProjectID, sessionID), msg)
		msg.Seq = nextSeqLocked(core, sessionID)
		core.messages[sessionID][i] = msg
		return msg, nil
	}
	return api.Message{}, errors.New("navigation message not found")
}
