package promptadmin

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleSessionCompact(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	report, err := s.Sessions.ForceCompact(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "session not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.SessionCompactResponse{
		Generation:        report.CompactionGeneration,
		TokensBefore:      report.TokensBefore,
		TokensAfter:       report.TokensAfter,
		ChunksCompacted:   report.ChunksCompacted,
		SessionCompacted:  report.SessionCompacted,
		TargetTokens:      report.TargetTokens,
		TargetMet:         report.TargetMet,
		Reason:            report.Reason,
		CacheHit:          report.CacheHit,
		MeasurementMethod: report.MeasurementMethod,
		TextTokensBefore:  report.TextTokensBefore,
		TextTokensAfter:   report.TextTokensAfter,
	})
}

func (s *Handler) HandleSessionContext(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctxInfo, err := s.Sessions.SessionContext(r.Context(), id)
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
