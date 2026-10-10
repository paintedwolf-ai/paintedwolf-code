package editoradmin

import (
	"net/http"

	"github.com/lycaon/lycaon/internal/api/requestscope"
)

// editorAffiliation answers false after writing the failure when the session
// store cannot be read.
func (s *Handler) editorAffiliation(w http.ResponseWriter, r *http.Request, projectID string, sessionID *string) (string, int, bool) {
	requested := ""
	if sessionID != nil {
		requested = *sessionID
	}
	session, turn, err := requestscope.ChatAffiliation(r.Context(), s.SessionStore, projectID, requested)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return "", 0, false
	}
	return session, turn, true
}
