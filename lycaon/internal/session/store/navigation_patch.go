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
func (s *SQL) PatchMessageNavigation(ctx context.Context, sessionID, messageID, contentHash string, expected, refs []api.NavigationReference) (api.Message, error) {
	unlock := s.lockMutation(sessionID)
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
	projectForScreen, err := s.queries.GetSessionProjectID(ctx, sessionID)
	if err != nil {
		return api.Message{}, err
	}
	msg = screenMessageForStore(messageScreenContext(ctx, projectForScreen, sessionID), msg)
	encoded, err := sourceref.EncodeMetadata(msg.NavigationRefs, msg.SourceContext)
	if err != nil {
		return api.Message{}, err
	}
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
	if err := s.outbox.EnqueueTx(ctx, tx, api.EventTopicMessage, events.PublishKey{Project: projectID, Session: sessionID}, api.MessageEvent{SessionID: sessionID, Op: api.MessageChangePatch, Seq: seq, Message: observer}); err != nil {
		return api.Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return api.Message{}, err
	}
	s.outbox.Notify()
	return msg, nil
}

func (s *Memory) PatchMessageNavigation(ctx context.Context, sessionID, messageID, contentHash string, expected, refs []api.NavigationReference) (api.Message, error) {
	if err := ctx.Err(); err != nil {
		return api.Message{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return api.Message{}, ErrSessionNotFound
	}
	for i, msg := range s.messages[sessionID] {
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
		msg = screenMessageForStore(messageScreenContext(ctx, s.sessions[sessionID].ProjectID, sessionID), msg)
		msg.Seq = s.nextSeqLocked(sessionID)
		s.messages[sessionID][i] = msg
		return msg, nil
	}
	return api.Message{}, errors.New("navigation message not found")
}
