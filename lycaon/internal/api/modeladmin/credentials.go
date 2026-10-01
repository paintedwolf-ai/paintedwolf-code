package modeladmin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/llm"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) SetCredential(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "provider_id")
	var req wire.SetProviderCredentialRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if _, ok := s.service.Catalog.Get(id); !ok {
		s.responses.Fail(w, wire.ApiErrorCodeProviderNotFound, "provider not found")
		return
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "api_key must not be empty")
		return
	}
	if err := s.service.SetProviderCredential(r.Context(), id, apiKey); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	s.publishProviderEvent(r.Context(), "credential_updated", id)
	httpio.WriteJSON(w, http.StatusOK, s.findProviderMeta(r.Context(), id))
}

func (s *Handler) DeleteCredential(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "provider_id")
	if err := s.service.DeleteProviderCredential(r.Context(), id); err != nil {
		var notFound *llm.ProviderNotFoundError
		if errors.As(err, &notFound) {
			s.responses.Fail(w, wire.ApiErrorCodeProviderNotFound, "provider not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	s.publishProviderEvent(r.Context(), "credential_deleted", id)
	w.WriteHeader(http.StatusNoContent)
}
