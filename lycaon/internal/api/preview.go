package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// handleListSessionPreviews returns held preview attachments.
func (s *Conversation) handleListSessionPreviews(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if !requestscope.SessionExists(s.sessionStore, s.responses, w, r, id) {
		return
	}
	events := s.preview.SnapshotForSession(r.Context(), id)
	if events == nil {
		events = []wire.PreviewAttachment{}
	}
	httpio.WriteJSON(w, http.StatusOK, wire.PreviewListResponse{Previews: events})
}

// handleWatchPreview toggles CDP screencast when Den's preview pane is visible.
func (s *Conversation) handleWatchPreview(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if !requestscope.SessionExists(s.sessionStore, s.responses, w, r, id) {
		return
	}
	var req wire.PreviewWatchRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if strings.TrimSpace(req.PageID) == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "preview watch page_id required")
		return
	}
	result := s.preview.SetWatching(r.Context(), id, req.Watching, req.PageID)
	httpio.WriteJSON(w, http.StatusOK, result)
}
