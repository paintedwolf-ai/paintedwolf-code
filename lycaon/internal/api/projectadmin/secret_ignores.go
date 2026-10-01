package projectadmin

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/projectignore"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) writeSecretIgnoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, projectignore.ErrReviewUnavailable):
		s.responses.Fail(w, wire.ApiErrorCodeSecretReviewNotFound, "the secret review is no longer available")
	case errors.Is(err, projectignore.ErrRootNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeRootNotFound, "the selected project folder is no longer attached")
	case errors.Is(err, projectignore.ErrEntryNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeIgnoreEntryNotFound, "that declaration is no longer in this project's ignore file")
	case errors.Is(err, projectignore.ErrConflict):
		s.responses.Fail(w, wire.ApiErrorCodeIgnoreFileChanged, "ignore file changed; reload before saving")
	case errors.Is(err, projectignore.ErrUntrusted):
		s.responses.Fail(w, wire.ApiErrorCodeTrustSurfaceOff, "this project's ignores are not trusted")
	case errors.Is(err, projectignore.ErrProtected):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "protected credentials cannot be ignored")
	case errors.Is(err, projectignore.ErrInvalid):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "the ignore entry is invalid")
	default:
		s.responses.InternalError(w, r, err)
	}
}

func (s *Handler) writeSecretIgnoresWithStatus(w http.ResponseWriter, r *http.Request, projectID, filterRootID string, status int) {
	catalog, err := s.SecretIgnores.List(r.Context(), projectID)
	if err != nil {
		s.writeSecretIgnoreError(w, r, err)
		return
	}
	rules := make([]wire.SecretIgnoreRule, 0, len(catalog.Rules)+len(catalog.Invalid))
	for _, rule := range catalog.Rules {
		if filterRootID != "" && rule.RootID != filterRootID {
			continue
		}
		rules = append(rules, wire.SecretIgnoreRule{
			ID:        rule.Key(),
			Value:     rule.Value,
			Reason:    rule.Reason,
			ExpiresOn: rule.Expires,
			RootID:    rule.RootID,
			Path:      rule.Path,
			Status:    rule.Status,
		})
	}
	for i, defect := range catalog.Invalid {
		if filterRootID != "" && defect.RootID != filterRootID {
			continue
		}
		id := defect.ID
		if id == "" {
			id = fmt.Sprintf("invalid-%d", i+1)
		}
		rules = append(rules, wire.SecretIgnoreRule{
			ID:     id,
			Value:  "",
			Reason: defect.Reason,
			RootID: defect.RootID,
			Path:   defect.Path,
			Status: "invalid",
		})
	}
	httpio.WriteJSON(w, status, wire.SecretIgnoreList{Rules: rules})
}

func (s *Handler) HandleListSecretIgnores(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.Registry, s.responses, w, r)
	if !ok {
		return
	}
	rootID := strings.TrimSpace(r.URL.Query().Get("root_id"))
	if rootID != "" {
		if _, err := uuid.Parse(rootID); err != nil {
			s.responses.InvalidQueryParam(w, "root_id", "must be a uuid")
			return
		}
	}
	s.writeSecretIgnoresWithStatus(w, r, p.ID, rootID, http.StatusOK)
}

func (s *Handler) HandleCreateProjectSecretIgnore(w http.ResponseWriter, r *http.Request) {
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, chi.URLParam(r, "id"))
	if release == nil {
		return
	}
	defer release()
	p, ok := requestscope.ProjectByURLID(s.Registry, s.responses, w, r)
	if !ok {
		return
	}
	var body wire.AddSecretIgnoreRequest
	if err := httpio.DecodeJSON(w, r, &body); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	rootID := strings.TrimSpace(body.RootID)
	if _, err := uuid.Parse(rootID); err != nil {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "root_id"}, "root_id must be a uuid")
		return
	}
	err := s.SecretIgnores.Add(r.Context(), p.ID, rootID, projectignore.SecretEntry{
		ID:      body.Entry.ID,
		Value:   body.Entry.Value,
		Reason:  body.Entry.Reason,
		Expires: body.Entry.ExpiresOn,
	})
	if err != nil {
		s.writeSecretIgnoreError(w, r, err)
		return
	}
	s.writeSecretIgnoresWithStatus(w, r, p.ID, "", http.StatusCreated)
}

func (s *Handler) HandleDeleteProjectSecretIgnore(w http.ResponseWriter, r *http.Request) {
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, chi.URLParam(r, "id"))
	if release == nil {
		return
	}
	defer release()
	p, ok := requestscope.ProjectByURLID(s.Registry, s.responses, w, r)
	if !ok {
		return
	}
	rootID := strings.TrimSpace(r.URL.Query().Get("root_id"))
	if _, err := uuid.Parse(rootID); err != nil {
		s.responses.InvalidQueryParam(w, "root_id", "is required and must be a uuid")
		return
	}
	err := s.SecretIgnores.Remove(r.Context(), p.ID, rootID, chi.URLParam(r, "entry_id"))
	if err != nil {
		s.writeSecretIgnoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Handler) HandleSecretIgnoreCandidate(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.Registry, s.responses, w, r)
	if !ok {
		return
	}
	value, err := s.SecretIgnores.Reviews.Value(p.ID, chi.URLParam(r, "candidate_id"))
	if err != nil {
		s.writeSecretIgnoreError(w, r, err)
		return
	}
	if s.SecretIgnores.Protected != nil && s.SecretIgnores.Protected(r.Context(), p.ID, value) {
		s.writeSecretIgnoreError(w, r, projectignore.ErrProtected)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpio.WriteJSON(w, http.StatusOK, wire.SecretIgnoreCandidate{Value: value})
}
