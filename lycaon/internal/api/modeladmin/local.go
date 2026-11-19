package modeladmin

import (
	"context"
	"github.com/lycaon/lycaon/internal/llm"
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

func (s *Handler) localProvider(w http.ResponseWriter, r *http.Request) bool {
	if chi.URLParam(r, "provider_id") != llm.IncludedProviderID {
		s.responses.Fail(w, wire.ApiErrorCodeProviderNotFound, "unknown local model provider")
		return false
	}
	return true
}

func (s *Handler) GetLocalModelStatus(w http.ResponseWriter, r *http.Request) {
	if s.localProvider(w, r) {
		httpio.WriteJSON(w, http.StatusOK, s.service.Registry.LocalGenerationStatus())
	}
}

func (s *Handler) SelectLocalModel(w http.ResponseWriter, r *http.Request) {
	if !s.localProvider(w, r) {
		return
	}
	var req wire.LocalModelSelectionRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	err := s.selectLocalModel(r.Context(), req)
	s.modelOperationResponse(w, err)
}

func (s *Handler) selectLocalModel(ctx context.Context, req wire.LocalModelSelectionRequest) error {
	ctx = context.WithoutCancel(ctx)
	return s.service.SelectLocalModel(req, func() { //nolint:contextcheck // Accepted transfers continue after the request returns.
		s.publishProviderEvent(ctx, "models_refreshed", llm.IncludedProviderID)
	})
}

func (s *Handler) CancelLocalModelTransfer(w http.ResponseWriter, r *http.Request) {
	if s.localProvider(w, r) {
		s.modelOperationResponse(w, s.service.Registry.CancelLocalModelTransfer(r.Context()))
	}
}

func (s *Handler) RemoveLocalModel(w http.ResponseWriter, r *http.Request) {
	if !s.localProvider(w, r) {
		return
	}
	s.modelOperationResponse(w, s.OptOutLocalModel(r.Context()))
}

func (s *Handler) OptOutLocalModel(ctx context.Context) error {
	err := s.service.Registry.RemoveLocalModel(ctx)
	if err == nil {
		s.service.ResetPlanes()
		s.publishProviderEvent(ctx, "models_refreshed", llm.IncludedProviderID)
	}
	return err
}

func (s *Handler) ImportLocalModel(ctx context.Context, model, path string) error {
	return s.selectLocalModel(ctx, wire.LocalModelSelectionRequest{Model: model, BundlePath: path})
}

func (s *Handler) modelOperationResponse(w http.ResponseWriter, err error) {
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, err.Error())
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.service.Registry.LocalGenerationStatus())
}
