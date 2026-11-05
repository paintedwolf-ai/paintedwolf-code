package modeladmin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) RepairLocalGeneration(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "provider_id")
	entry, exists := s.service.Catalog.Get(id)
	if !exists {
		s.responses.Fail(w, wire.ApiErrorCodeProviderNotFound, "provider not found")
		return
	}
	if entry.Kind != "nativegen" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "provider does not use bundled local generation")
		return
	}
	err := s.service.Registry.RepairLocalGeneration(r.Context(), id)
	if err != nil {
		s.responses.Logger.InfoContext(r.Context(), "local generation repair failed", "provider_id", id, "err", err)
	} else {
		s.service.ResetPlanes()
	}
	s.publishProviderEvent(r.Context(), "models_refreshed", id)
	httpio.WriteJSON(w, http.StatusOK, wire.LocalGenerationRepairResult{
		OK: err == nil, Status: s.service.Registry.LocalGenerationStatus(),
	})
}
