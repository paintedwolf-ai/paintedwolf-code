package sessionadmin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// HandleMarkSessionSeen stamps host time so clients cannot mark future turns read.
// It also updates the project's recents order.
func (s *Handler) HandleMarkSessionSeen(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	seenAt := time.Now().UTC()
	err := s.Store.UpdateSession(r.Context(), id, func(sess *wire.Session) {
		sess.SeenAt = &seenAt
	})
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "chat not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	// Complete the response lookup after client disconnect.
	ctx := context.WithoutCancel(r.Context())
	sess, err := s.Store.Get(ctx, id)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if sess.ProjectID != "" {
		if err := s.Projects.TouchLastOpened(ctx, sess.ProjectID); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		projectview.PublishTouch(s.Projects, s.Events, ctx, sess.ProjectID)
	}
	httpio.WriteJSON(w, http.StatusOK, sess)
}
