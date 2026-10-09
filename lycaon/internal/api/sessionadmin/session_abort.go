package sessionadmin

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleAbortSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req wire.AbortSessionRequest
	if _, err := httpio.DecodeOptionalJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if err := s.Sessions.Stops.Abort(r.Context(), id, req.Reason); err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "chat not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	// Drain retained queued turns after stopping.
	s.Prompt.MaybeDrainQueue(context.WithoutCancel(r.Context()), id)
	// Complete the response lookup after client disconnect.
	readCtx := context.WithoutCancel(r.Context())
	sess, err := s.Store.Get(readCtx, id)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	s.SessionView.EnrichSession(readCtx, sess)
	httpio.WriteJSON(w, http.StatusOK, sess)
}
