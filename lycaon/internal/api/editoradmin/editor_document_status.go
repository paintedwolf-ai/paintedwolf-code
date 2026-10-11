package editoradmin

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleReadEditorDocumentStatuses(w http.ResponseWriter, r *http.Request) {
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return
	}
	defer release()
	p, ok := s.editorProject(w, r)
	if !ok {
		return
	}
	var req wire.EditorDocumentStatusesRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if len(req.DocumentIds) == 0 || len(req.DocumentIds) > 64 {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "Document status reads require between 1 and 64 identities.")
		return
	}
	for _, id := range req.DocumentIds {
		if _, err := uuid.Parse(id); err != nil {
			s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "Invalid document identity.")
			return
		}
	}
	result, err := s.EditorDocuments.Statuses(r.Context(), p.ID, req.DocumentIds)
	if err != nil {
		s.writeEditorDocumentError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, result)
}

func (s *Handler) HandleReplaceEditorDocumentRetention(w http.ResponseWriter, r *http.Request) {
	var req wire.ReplaceEditorDocumentRetentionRequest
	var raw map[string]any
	if err := httpio.DecodeJSONWithRaw(w, r, &req, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !s.responses.RequireJSONObjectKeys(w, raw, "client_id", "retained_clients", "document_ids") {
		return
	}
	if strings.TrimSpace(req.ClientID) == "" {
		s.responses.InvalidField(w, "client_id", "is required")
		return
	}
	if len(req.DocumentIds) > 16384 {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "Too many document references.")
		return
	}
	for _, id := range req.DocumentIds {
		if _, err := uuid.Parse(id); err != nil {
			s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "Invalid document identity.")
			return
		}
	}
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return
	}
	defer release()
	p, ok := s.editorProject(w, r)
	if !ok {
		return
	}
	live := func(clientID string) bool { return s.EditorClients.StreamCount(clientID) > 0 }
	if err := s.EditorDocuments.ReplaceRetention(r.Context(), p.ID, req.ClientID, req.DocumentIds, req.RetainedClients, live); err != nil {
		s.writeEditorDocumentError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
