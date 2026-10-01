package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Server) handleSessionFindings(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sess, err := s.sessionStore.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "session not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	root := session.RootSessionID(r.Context(), s.sessionStore, sess.ID)
	rows, err := s.sessions.ListFindings(r.Context(), root, findings.DefaultListCap)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, findings.BuildDigestFromRows(rows, root))
}
