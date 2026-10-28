package sessionadmin

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var programLimitBounds = httpio.MustPageLimit(20, 1, 100)

func (s *Transcript) HandleGetCommandProgramReceipt(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	if _, err := s.Store.Get(r.Context(), sessionID); err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "chat not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	reader, ok := s.Invocations.(invocation.ProgramReader)
	if !ok {
		s.responses.InternalError(w, r, errors.New("program receipt reader is unavailable"))
		return
	}
	query, err := httpio.ReadPageQuery(r, programLimitBounds)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	result, err := reader.ReadProgramPage(r.Context(), sessionID, chi.URLParam(r, "program_id"), query.Cursor, query.Limit)
	if err != nil {
		switch {
		case errors.Is(err, pagecursor.ErrInvalid), errors.Is(err, pagecursor.ErrExpired):
			s.responses.PageCursorError(w, r, "cursor", err)
		case errors.Is(err, invocation.ErrProgramNotFound):
			http.NotFound(w, r)
		default:
			s.responses.InternalError(w, r, err)
		}
		return
	}
	httpio.WriteJSON(w, http.StatusOK, result)
}
