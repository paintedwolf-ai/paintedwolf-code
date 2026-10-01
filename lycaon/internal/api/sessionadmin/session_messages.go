package sessionadmin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var messageLimitBounds = httpio.MustPageLimit(100, 1, 500)

func (s *Handler) HandleListSessionMessages(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	q, ok := s.parseTranscriptPageQuery(w, r, id)
	if !ok {
		return
	}
	page, err := s.Sessions.GetTranscriptPage(r.Context(), id, q)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "chat not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	if page.Messages == nil {
		page.Messages = []wire.Message{}
	}
	httpio.WriteJSON(w, http.StatusOK, page)
}

// transcriptPositionParams are the mutually exclusive ways to place a
// transcript window: a continuation cursor, a message anchor, or an end.
var transcriptPositionParams = []string{"before", "after", "before_message_id", "after_message_id", "from"}

func (s *Handler) parseTranscriptPageQuery(w http.ResponseWriter, r *http.Request, sessionID string) (wire.TranscriptPageQuery, bool) {
	var q wire.TranscriptPageQuery
	wq, err := httpio.ReadWindowQuery(r, messageLimitBounds)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return q, false
	}
	q.Limit = wq.Limit
	position := map[string]string{}
	for _, param := range transcriptPositionParams {
		value, _, err := httpio.SingleQueryValue(r, param)
		if err != nil {
			s.responses.InvalidQuery(w, err)
			return q, false
		}
		if value == "" {
			continue
		}
		if len(position) > 0 {
			s.responses.InvalidQueryParam(w, param, "must not be combined with another window position")
			return q, false
		}
		position[param] = value
	}
	switch {
	case position["before"] != "":
		pos, err := store.MessagePages.Decode(position["before"], pagecursor.Scope(sessionID))
		if err != nil {
			s.responses.PageCursorError(w, r, "before", err)
			return q, false
		}
		q.Before = &pos.Ord
	case position["after"] != "":
		pos, err := store.MessagePages.Decode(position["after"], pagecursor.Scope(sessionID))
		if err != nil {
			s.responses.PageCursorError(w, r, "after", err)
			return q, false
		}
		q.After = &pos.Ord
	case position["before_message_id"] != "":
		ord, ok := s.transcriptAnchorOrd(w, r, sessionID, "before_message_id", position["before_message_id"])
		if !ok {
			return q, false
		}
		q.Before = &ord
	case position["after_message_id"] != "":
		ord, ok := s.transcriptAnchorOrd(w, r, sessionID, "after_message_id", position["after_message_id"])
		if !ok {
			return q, false
		}
		q.After = &ord
	case position["from"] == "oldest":
		start := int64(0)
		q.After = &start
	case position["from"] != "" && position["from"] != "newest":
		s.responses.InvalidQueryParam(w, "from", "must be newest or oldest")
		return q, false
	}

	if workerID := strings.TrimSpace(r.URL.Query().Get("worker_id")); workerID != "" {
		if _, err := uuid.Parse(workerID); err != nil {
			s.responses.InvalidQuery(w, &httpio.QueryParameterError{Parameter: "worker_id", Reason: "must be a UUID"})
			return q, false
		}
		q.WorkerID = workerID
	}
	return q, true
}

// transcriptAnchorOrd places a window beside a message of this chat.
func (s *Handler) transcriptAnchorOrd(w http.ResponseWriter, r *http.Request, sessionID, param, messageID string) (int64, bool) {
	if _, err := uuid.Parse(messageID); err != nil {
		s.responses.InvalidQueryParam(w, param, "must be a UUID")
		return 0, false
	}
	msg, err := s.Store.GetMessage(r.Context(), sessionID, messageID)
	switch {
	case errors.Is(err, store.ErrMessageNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeMessageNotFound, "That message is not in this chat.")
		return 0, false
	case errors.Is(err, store.ErrSessionNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "chat not found")
		return 0, false
	case err != nil:
		s.responses.InternalError(w, r, err)
		return 0, false
	}
	return msg.Ord, true
}
