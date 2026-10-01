package sourceapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// UserSourceChatAffiliation associates mutations with an active chat turn.
func (s *Handler) UserSourceChatAffiliation(r *http.Request) (sessionID string, turn int) {
	if held, ok := r.Context().Value(sourceRequestAffiliationKey{}).(sourceRequestAffiliation); ok {
		return held.sessionID, held.turn
	}
	sessionID, turn, err := s.chatAffiliation(r.Context(), "", r.URL.Query().Get("session_id"))
	if err != nil {
		return "", 0
	}
	return sessionID, turn
}

// chatAffiliation names the focused chat and the turn live when the change
// lands. A chat the host cannot place leaves the change unaffiliated; a
// person's text is never refused for its affiliation.
func (s *Handler) chatAffiliation(ctx context.Context, projectID, sessionID string) (string, int, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", 0, nil
	}
	sess, err := s.SessionStore.Get(ctx, sessionID)
	if errors.Is(err, store.ErrSessionNotFound) || (err == nil && sess == nil) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	if projectID != "" && sess.ProjectID != projectID {
		return "", 0, nil
	}
	if sess.Status != wire.SessionStatusBusy || sess.CurrentTurn < 1 {
		return sessionID, 0, nil
	}
	return sessionID, sess.CurrentTurn, nil
}

// editorAffiliation answers false after writing the failure when the session
// store cannot be read.
func (s *Handler) editorAffiliation(w http.ResponseWriter, r *http.Request, projectID string, sessionID *string) (string, int, bool) {
	requested := ""
	if sessionID != nil {
		requested = *sessionID
	}
	session, turn, err := s.chatAffiliation(r.Context(), projectID, requested)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return "", 0, false
	}
	return session, turn, true
}
