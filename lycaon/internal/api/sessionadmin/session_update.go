package sessionadmin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Lifecycle) HandleUpdateSession(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	var req wire.UpdateSessionRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	var (
		sess *wire.Session
		err  error
	)
	fields := 0
	if req.Title != nil {
		fields++
	}
	if req.Archived != nil {
		fields++
	}
	if req.Pinned != nil {
		fields++
	}
	if req.PinPosition != nil {
		fields++
	}
	if fields != 1 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "patch requires exactly one of title, archived, pinned, or pin_position")
		return
	}
	if req.PinPosition != nil && *req.PinPosition < 1 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "pin_position must be at least 1")
		return
	}
	switch {
	case req.Title != nil:
		sess, err = s.Sessions.SetTitle(r.Context(), id, *req.Title)
	case req.Archived != nil:
		sess, err = s.Sessions.SetArchived(r.Context(), id, *req.Archived)
	case req.Pinned != nil:
		sess, err = s.Sessions.SetPinned(r.Context(), id, *req.Pinned)
	case req.PinPosition != nil:
		sess, err = s.Sessions.MovePinned(r.Context(), id, *req.PinPosition)
	}
	if err != nil {
		s.writeSessionLifecycleError(w, r, err)
		return
	}
	s.SessionView.HydrateSessionWorkspace(r.Context(), sess)
	s.SessionView.EnrichSession(r.Context(), sess)
	httpio.WriteJSON(w, http.StatusOK, sess)
}

func (s *Lifecycle) HandleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if err := s.Sessions.DeleteSession(r.Context(), id); err != nil {
		s.writeSessionLifecycleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Lifecycle) writeSessionLifecycleError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, session.ErrInvalidDisplayTitle):
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "title must be 1–80 characters without control characters")
	case errors.Is(err, session.ErrWorkerChildLifecycle), errors.Is(err, store.ErrWorkerChildPin):
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "worker child sessions have no chat lifecycle")
	case errors.Is(err, store.ErrSessionArchived):
		s.responses.Fail(w, wire.ApiErrorCodeSessionArchived, "Unarchive this chat before pinning it.")
	case errors.Is(err, store.ErrSessionNotPinned):
		s.responses.Fail(w, wire.ApiErrorCodeSessionNotPinned, "Pin this chat before moving it.")
	case errors.Is(err, session.ErrSessionBusy):
		s.responses.Fail(w, wire.ApiErrorCodeSessionNotIdle, "session has a turn in flight — stop it first")
	case errors.Is(err, session.ErrSessionWorktreeBound):
		s.responses.Fail(w, wire.ApiErrorCodeWorktreeAlreadyBound, "Unbind this chat's worktree before deleting it.")
	case errors.Is(err, store.ErrSessionNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "chat not found")
	default:
		s.responses.InternalError(w, r, err)
	}
}
