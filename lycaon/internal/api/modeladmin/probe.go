package modeladmin

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/failure"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) TestProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "provider_id")
	if _, exists := s.service.Catalog.Get(id); !exists {
		s.responses.Fail(w, wire.ApiErrorCodeProviderNotFound, "provider not found")
		return
	}

	start := time.Now()
	_, err := s.service.Registry.TestConnectivity(r.Context(), id)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		code := wire.ApiErrorCodeModelCatalogRefreshFailed
		message := "The provider rejected the request or returned an invalid response."
		if _, notConfigured := failure.AsProviderNotConfigured(err); notConfigured {
			code = wire.ApiErrorCodeProviderNotConfigured
			message = "The provider is not configured."
		} else if httpclient.Unreachable(err) {
			code = wire.ApiErrorCodeModelCatalogUnreachable
			message = "Could not reach the provider."
		}
		s.responses.Logger.InfoContext(r.Context(), "provider test failed", "provider_id", id, "code", code, "err", err)
		httpio.WriteJSON(w, http.StatusOK, wire.ProviderProbeResult{
			OK:        false,
			Code:      code,
			Message:   message,
			LatencyMs: latency,
		})
		return
	}
	s.service.ResetPlanes()
	s.publishProviderEvent(r.Context(), "models_refreshed", id)
	httpio.WriteJSON(w, http.StatusOK, wire.ProviderProbeResult{
		OK:        true,
		LatencyMs: latency,
	})
}

func (s *Handler) RefreshModels(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "provider_id"))
	if _, ok := s.service.Catalog.Get(id); !ok {
		s.responses.Fail(w, wire.ApiErrorCodeProviderNotFound, "provider not found")
		return
	}
	if err := s.service.Registry.RefreshModels(r.Context(), id); err != nil {
		if _, notConfigured := failure.AsProviderNotConfigured(err); notConfigured {
			s.responses.Fail(w, wire.ApiErrorCodeProviderNotConfigured, "provider is not configured")
			return
		}
		if httpclient.Unreachable(err) {
			s.responses.Fail(w, wire.ApiErrorCodeModelCatalogUnreachable, "could not reach the provider to refresh the model list")
			return
		}
		s.responses.Fail(w, wire.ApiErrorCodeModelCatalogRefreshFailed, "provider rejected or returned an invalid model catalog response")
		return
	}
	s.publishProviderEvent(r.Context(), "models_refreshed", id)
	httpio.WriteJSON(w, http.StatusOK, s.findProviderMeta(r.Context(), id))
}
