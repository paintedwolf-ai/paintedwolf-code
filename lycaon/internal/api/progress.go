package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/store"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Conversation) handleSessionProgress(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.sessionStore.Get(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "session not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	root := sessiontree.RootID(r.Context(), s.sessionStore, id)
	httpio.WriteJSON(w, http.StatusOK, progress.BuildDigest(s.progressStore.Get(r.Context(), root), root))
}
