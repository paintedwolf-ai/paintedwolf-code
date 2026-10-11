package sessionadmin

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var invocationLimitBounds = httpio.MustPageLimit(50, 1, 500)

func (s *Transcript) HandleListSessionInvocations(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	if _, err := s.Store.Get(r.Context(), sessionID); err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "chat not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	pq, err := httpio.ReadPageQuery(r, invocationLimitBounds)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	list, err := s.Invocations.ListSessionPage(r.Context(), sessionID, pq.Cursor, pq.Limit)
	if err != nil {
		if errors.Is(err, pagecursor.ErrInvalid) || errors.Is(err, pagecursor.ErrExpired) {
			s.responses.PageCursorError(w, r, "cursor", err)
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	if list.Invocations == nil {
		list.Invocations = []wire.InvocationReceipt{}
	}
	httpio.WriteJSON(w, http.StatusOK, list)
}
