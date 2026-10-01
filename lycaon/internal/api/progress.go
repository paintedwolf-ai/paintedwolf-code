package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Server) handleSessionProgress(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.sessionStore.Get(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "session not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	root := session.RootSessionID(r.Context(), s.sessionStore, id)
	httpio.WriteJSON(w, http.StatusOK, progress.BuildDigest(s.progressStore.Get(r.Context(), root), root))
}
