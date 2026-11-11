package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// GetMessages returns a copy of session message history.
func (s *memoryTranscript) GetMessages(ctx context.Context, id string) ([]api.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	core := s.core
	core.mu.RLock()
	defer core.mu.RUnlock()

	if _, ok := core.sessions[id]; !ok {
		return nil, ErrSessionNotFound
	}
	msgs := core.messages[id]
	out := make([]api.Message, len(msgs))
	copy(out, msgs)
	return out, nil
}

// GetWorkerJobMessages reads only rows associated with one worker job.
func (s *memoryTranscript) GetWorkerJobMessages(ctx context.Context, sessionID, workerJobID string) ([]api.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	workerJobID = strings.TrimSpace(workerJobID)
	if workerJobID == "" {
		return nil, fmt.Errorf("worker job id required")
	}
	core := s.core
	core.mu.RLock()
	defer core.mu.RUnlock()
	if _, ok := core.sessions[sessionID]; !ok {
		return nil, ErrSessionNotFound
	}
	out := make([]api.Message, 0)
	for _, message := range core.messages[sessionID] {
		if strings.TrimSpace(message.WorkerID) == workerJobID {
			out = append(out, message)
		}
	}
	return out, nil
}

// GetMessage reads one transcript projection by stable identity.
func (s *memoryTranscript) GetMessage(ctx context.Context, sessionID, messageID string) (api.Message, error) {
	if err := ctx.Err(); err != nil {
		return api.Message{}, err
	}
	core := s.core
	core.mu.RLock()
	defer core.mu.RUnlock()
	if _, ok := core.sessions[sessionID]; !ok {
		return api.Message{}, ErrSessionNotFound
	}
	for _, message := range core.messages[sessionID] {
		if message.ID == messageID {
			return message, nil
		}
	}
	return api.Message{}, ErrMessageNotFound
}

// GetTranscriptPage returns one Ord window plus the session's transcript watermark.
func (s *memoryTranscript) GetTranscriptPage(ctx context.Context, id string, q api.TranscriptPageQuery) (api.SessionTranscriptPage, error) {
	if err := ctx.Err(); err != nil {
		return api.SessionTranscriptPage{}, err
	}

	core := s.core
	core.mu.RLock()
	defer core.mu.RUnlock()

	if _, ok := core.sessions[id]; !ok {
		return api.SessionTranscriptPage{}, ErrSessionNotFound
	}
	all := filterWorkerMessages(core.messages[id], q.WorkerID)
	window := pageAscendingWindow(all, q)
	if window == nil {
		window = []api.Message{}
	}
	page := api.SessionTranscriptPage{
		Messages:   window,
		TurnClocks: map[string]api.TurnClock{},
		TurnLoads:  map[string][]api.TurnLoad{},
		Watermark:  core.transcriptSeq[id],
		OrdBounds:  messageOrdBounds(all),
	}
	if strings.TrimSpace(q.WorkerID) == "" {
		page.TurnClocks = turnClocksForMessagesLocked(core, id, page.Messages)
	}
	hasBefore, hasAfter := memoryTranscriptWindowHasMore(all, page.Messages, q)
	if err := SetTranscriptPageCursors(id, &page, hasBefore, hasAfter, q); err != nil {
		return api.SessionTranscriptPage{}, err
	}
	return page, nil
}

func filterWorkerMessages(all []api.Message, workerID string) []api.Message {
	workerJobID := strings.TrimSpace(workerID)
	if workerJobID == "" {
		return all
	}
	filtered := make([]api.Message, 0, len(all))
	for _, message := range all {
		if strings.TrimSpace(message.WorkerID) == workerJobID {
			filtered = append(filtered, message)
		}
	}
	return filtered
}

func memoryTranscriptWindowHasMore(all, window []api.Message, q api.TranscriptPageQuery) (hasBefore, hasAfter bool) {
	if len(window) > 0 {
		return windowHasMore(all, window)
	}
	switch {
	case q.Before != nil:
		for _, m := range all {
			if m.Ord < *q.Before {
				hasBefore = true
			}
			if m.Ord >= *q.Before {
				hasAfter = true
			}
		}
	case q.After != nil:
		for _, m := range all {
			if m.Ord <= *q.After {
				hasBefore = true
			}
			if m.Ord > *q.After {
				hasAfter = true
			}
		}
	}
	return hasBefore, hasAfter
}

// nextSeqLocked bumps the session's transcript mutation clock. Callers hold s.mu.
func nextSeqLocked(s *memoryCore, sessionID string) int64 {
	s.transcriptSeq[sessionID]++
	return s.transcriptSeq[sessionID]
}

// nextOrdLocked bumps the session's immutable creation ordinal; only appends
// assign one. Callers hold s.mu.
func nextOrdLocked(s *memoryCore, sessionID string) int64 {
	s.transcriptOrd[sessionID]++
	return s.transcriptOrd[sessionID]
}

// AppendMessages stamps sequence numbers into the caller's slice for publication.
func (s *memoryTranscript) AppendMessages(ctx context.Context, id string, msgs ...api.Message) error {
	return appendMessagesMemory(ctx, s.core, id, msgs...)
}

func appendMessagesMemory(ctx context.Context, s *memoryCore, id string, msgs ...api.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	if err := rejectDuplicateAppendIDs(msgs); err != nil {
		return err
	}
	if err := rejectExistingMessageIDs(s.messages[id], msgs); err != nil {
		return err
	}
	screenMessagesForStore(messageScreenContext(ctx, sess.ProjectID, id), msgs)
	for i := range msgs {
		msgs[i] = api.NormalizeMessageProvenance(msgs[i])
		if err := requireWorkflowRunStamp(msgs[i]); err != nil {
			return err
		}
		msgs[i].Seq = nextSeqLocked(s, id)
		msgs[i].Ord = nextOrdLocked(s, id)
		if api.IsUserIntentMessage(msgs[i]) {
			if s.userTurns[id] == nil {
				s.userTurns[id] = make(map[string]int)
			}
			s.userTurnSequence[id]++
			s.userTurns[id][msgs[i].ID] = s.userTurnSequence[id]
		}
	}
	s.messages[id] = append(s.messages[id], msgs...)
	sess.UpdatedAt = time.Now().UTC()
	if appendsActivity(msgs) {
		sess.ActivityAt = sess.UpdatedAt
	}
	return nil
}

// UpdateMessage replaces one message by ID.
func (s *memoryTranscript) UpdateMessage(ctx context.Context, sessionID, messageID string, msg api.Message) (api.Message, error) {
	return updateMessageMemory(ctx, s.core, sessionID, messageID, msg)
}

func updateMessageMemory(ctx context.Context, s *memoryCore, sessionID, messageID string, msg api.Message) (api.Message, error) {
	if err := ctx.Err(); err != nil {
		return api.Message{}, err
	}
	msg = api.NormalizeMessageProvenance(msg)
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return api.Message{}, ErrSessionNotFound
	}
	msg = screenMessageForStore(messageScreenContext(ctx, sess.ProjectID, sessionID), msg)
	msgs := s.messages[sessionID]
	for i := range msgs {
		if msgs[i].ID == messageID {
			msg.Seq = nextSeqLocked(s, sessionID)
			// Patches retain the original ordinal, timestamp, and author.
			msg.Ord = msgs[i].Ord
			msg.CreatedAt = msgs[i].CreatedAt
			msg.AuthorPersonID = msgs[i].AuthorPersonID
			msgs[i] = msg
			s.messages[sessionID] = msgs
			if view, ok := s.compactionViews[sessionID]; ok && msg.Ord <= view.CoveredThroughOrd {
				delete(s.compactionViews, sessionID)
			}
			sess.UpdatedAt = time.Now().UTC()
			return msg, nil
		}
	}
	return api.Message{}, fmt.Errorf("%w: %s", ErrMessageNotFound, messageID)
}

// PatchLiveProjection writes streaming prose and in-flight tool_calls. Seq is unchanged.
func (s *memoryTranscript) PatchLiveProjection(ctx context.Context, sessionID, messageID, content string, toolCalls []api.ToolCall) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	core := s.core
	core.mu.Lock()
	defer core.mu.Unlock()
	if _, ok := core.sessions[sessionID]; !ok {
		return ErrSessionNotFound
	}
	msgs := core.messages[sessionID]
	for i := range msgs {
		if msgs[i].ID == messageID {
			msgs[i].Content = content
			if toolCalls != nil {
				msgs[i].ToolCalls = append([]api.ToolCall(nil), toolCalls...)
			}
			core.messages[sessionID] = msgs
			if view, ok := core.compactionViews[sessionID]; ok && msgs[i].Ord <= view.CoveredThroughOrd {
				delete(core.compactionViews, sessionID)
			}
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrMessageNotFound, messageID)
}
