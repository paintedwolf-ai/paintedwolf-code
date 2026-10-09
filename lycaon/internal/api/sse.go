package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Conversation) streamReplayMessage(w http.ResponseWriter, r *http.Request, sessionID, messageID string) (wire.Message, bool) {
	// The transcript owns message membership, including removals by rewind.
	msg, err := s.sessionStore.GetMessage(r.Context(), sessionID, messageID)
	if errors.Is(err, store.ErrMessageNotFound) || errors.Is(err, store.ErrSessionNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeMessageNotFound, "stream content not found")
		return wire.Message{}, false
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return wire.Message{}, false
	}
	return messageview.TranscriptMessage(msg), true
}

func (s *Conversation) handleStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	messageID := strings.TrimSpace(r.URL.Query().Get("message"))
	if !requestscope.SessionExists(s.sessionStore, s.responses, w, r, id) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeInternalError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if messageID == "" {
		writePromptChunk(w, flusher, wire.PromptStreamChunk{Done: true})
		return
	}

	if s.sessions.Streams().ActiveMessageID(id) == messageID {
		s.followLiveStream(r, w, flusher, id, messageID)
		return
	}
	s.replayStream(w, r, flusher, id, messageID)
}

func (s *Conversation) replayStream(w http.ResponseWriter, r *http.Request, flusher http.Flusher, sessionID, messageID string) {
	msg, ok := s.streamReplayMessage(w, r, sessionID, messageID)
	if !ok {
		return
	}
	content := msg.Content
	if msg.Role == wire.MessageRoleAssistant && len(msg.ToolCalls) > 0 {
		if writePromptChunk(w, flusher, wire.PromptStreamChunk{Reset: true, ToolCalls: msg.ToolCalls}) {
			writePromptChunk(w, flusher, wire.PromptStreamChunk{Done: true})
		}
		return
	}
	tokens, found := s.sessions.Streams().Tokens(messageID)
	if !found {
		if cached, cachedOK := s.sessions.Streams().Content(messageID); cachedOK {
			content = cached
		}
		if !writePromptChunk(w, flusher, wire.PromptStreamChunk{Token: content, Reset: true}) {
			return
		}
		writePromptChunk(w, flusher, wire.PromptStreamChunk{Done: true})
		return
	}
	if !replayPromptChunks(r.Context(), s.ShuttingDown(), w, flusher, tokens, 10*time.Millisecond) {
		return
	}
	writePromptChunk(w, flusher, wire.PromptStreamChunk{Done: true})
}

func (s *Conversation) followLiveStream(r *http.Request, w http.ResponseWriter, flusher http.Flusher, sessionID, messageID string) {
	ch, unsub := s.sessions.Streams().Subscribe(messageID)
	defer unsub()

	if s.sessions.Streams().ActiveMessageID(sessionID) != messageID {
		s.replayStream(w, r, flusher, sessionID, messageID)
		return
	}

	shuttingDown := s.ShuttingDown()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-shuttingDown:
			return
		case frame, ok := <-ch:
			if !ok {
				writePromptChunk(w, flusher, wire.PromptStreamChunk{Done: true})
				return
			}
			if frame.Done {
				writePromptChunk(w, flusher, wire.PromptStreamChunk{Done: true})
				return
			}
			chunk := wire.PromptStreamChunk{
				Token:     frame.Content,
				Reset:     true,
				ToolCalls: frame.ToolCalls,
			}
			if !writePromptChunk(w, flusher, chunk) {
				return
			}
		}
	}
}

// A nil shutdown channel disables the shutdown select case.
func replayPromptChunks(ctx context.Context, shuttingDown <-chan struct{}, w http.ResponseWriter, flusher http.Flusher, tokens []string, pace time.Duration) bool {
	for i, token := range tokens {
		select {
		case <-ctx.Done():
			return false
		case <-shuttingDown:
			return false
		default:
		}
		if !writePromptChunk(w, flusher, wire.PromptStreamChunk{Token: token, Reset: i == 0}) {
			return false
		}
		timer := time.NewTimer(pace)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-shuttingDown:
			timer.Stop()
			return false
		}
	}
	return true
}

func writePromptChunk(w http.ResponseWriter, flusher http.Flusher, chunk wire.PromptStreamChunk) bool {
	data, err := json.Marshal(chunk)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return false
	}
	flusher.Flush()
	return true
}
