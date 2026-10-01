// Package historyadmin serves history retention and protection operations.
package historyadmin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/historyretention"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Handler serves history storage status, retention policy, and pruning.
type Handler struct {
	service   *historyretention.Service
	responses *httpio.Responder
}

func New(service *historyretention.Service, responses *httpio.Responder) *Handler {
	httpio.RequireDependencies("historyadmin",
		httpio.Required{Name: "responses", Present: responses != nil},
		httpio.Required{Name: "service", Present: service != nil},
	)
	return &Handler{service: service, responses: responses}
}

func (s *Handler) serviceError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, historyretention.ErrPreviewChanged) {
		s.responses.Fail(w, wire.ApiErrorCodeHistoryPreviewChanged, "history storage changed after the preview; preview again")
		return
	}
	if errors.Is(err, historyretention.ErrInvalidPolicy) {
		s.responses.Logger.DebugContext(r.Context(), "history request rejected", "err", err)
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "the history retention policy or protection is not valid")
		return
	}
	s.responses.InternalError(w, r, err)
}

func (s *Handler) Status(w http.ResponseWriter, r *http.Request) {
	out, err := s.service.Status(r.Context())
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

func (s *Handler) Preview(w http.ResponseWriter, r *http.Request) {
	var request wire.HistoryRetentionRequest
	if err := httpio.DecodeJSON(w, r, &request); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	out, err := s.service.Preview(r.Context(), request, projectID)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

func (s *Handler) UpdateStorage(w http.ResponseWriter, r *http.Request) {
	var request wire.HistoryRetentionRequest
	if err := httpio.DecodeJSON(w, r, &request); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	out, err := s.service.SetPolicy(r.Context(), request, projectID)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

func (s *Handler) Prune(w http.ResponseWriter, r *http.Request) {
	var request wire.HistoryRetentionRequest
	if err := httpio.DecodeJSON(w, r, &request); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	out, err := s.service.Prune(r.Context(), request, projectID)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

func (s *Handler) CreateProtection(w http.ResponseWriter, r *http.Request) {
	var request wire.HistoryProtection
	if err := httpio.DecodeJSON(w, r, &request); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if err := s.service.Protect(r.Context(), request); err != nil {
		s.serviceError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusCreated, request)
}

func (s *Handler) DeleteProtection(w http.ResponseWriter, r *http.Request) {
	protectionID := strings.TrimSpace(chi.URLParam(r, "protection_id"))
	if protectionID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "protection_id is required")
		return
	}
	if err := s.service.DeleteProtection(r.Context(), protectionID); err != nil {
		if errors.Is(err, historyretention.ErrProtectionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeHistoryProtectionNotFound, "history protection not found")
			return
		}
		s.serviceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
