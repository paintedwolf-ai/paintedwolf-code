package promptadmin

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleCoordinatorContext(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctxInfo, err := s.Sessions.CoordinatorRunContext(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "session not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, ctxInfo)
}
