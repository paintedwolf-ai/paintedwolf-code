package sourceapi

import (
	"net/http"

	"github.com/lycaon/lycaon/internal/api/requestscope"
)

// UserSourceChatAffiliation associates mutations with an active chat turn.
func (s *Workspace) UserSourceChatAffiliation(r *http.Request) (sessionID string, turn int) {
	if held, ok := r.Context().Value(sourceRequestAffiliationKey{}).(sourceRequestAffiliation); ok {
		return held.sessionID, held.turn
	}
	sessionID, turn, err := requestscope.ChatAffiliation(r.Context(), s.SessionStore, "", r.URL.Query().Get("session_id"))
	if err != nil {
		return "", 0
	}
	return sessionID, turn
}
