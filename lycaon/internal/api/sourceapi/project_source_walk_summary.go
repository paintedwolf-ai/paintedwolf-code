package sourceapi

import (
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleGetProjectSourceWalkSummary(w http.ResponseWriter, r *http.Request) {
	project, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	project, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, project)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		s.responses.InvalidQueryParam(w, "session_id", "is required")
		return
	}
	ids := strings.Split(r.URL.Query().Get("message_ids"), ",")
	if len(ids) > 100 || strings.TrimSpace(ids[0]) == "" {
		s.responses.InvalidQueryParam(w, "message_ids", "must list 1 to 100 message ids")
		return
	}
	rows, err := s.SourceLedger.WalkSummary(r.Context(), project.ID, sessionID, ids)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.SourceWalkSummary{Turns: rows})
}
