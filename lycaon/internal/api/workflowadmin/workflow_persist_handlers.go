package workflowadmin

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandlePersistWorkflow(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	workflowID := chi.URLParam(r, "workflow_id")
	sess, ok := requestscope.Session(s.Store, s.responses, w, r, sessionID)
	if !ok {
		return
	}
	s.SessionView.HydrateSessionWorkspace(r.Context(), sess)
	var req wire.PersistWorkflowRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	result, err := s.Persister.Persist(r.Context(), workflowcomposition.PersistRequest{
		SessionID:      sessionID,
		ProjectDir:     sess.WorkspacePath,
		WorkflowID:     workflowID,
		Version:        req.Version,
		Confirm:        req.Confirm,
		Trigger:        req.Trigger,
		SessionPosture: sess.Posture,
		CreatedBy:      workflowdrafts.User,
	})
	if err != nil {
		var notConfirmed *workflowcomposition.PersistNotConfirmedError
		if errors.As(err, &notConfirmed) {
			s.responses.Fail(w, wire.ApiErrorCodePersistNotConfirmed, "confirm before saving this workflow")
			return
		}
		if errors.Is(err, workflowdrafts.ErrNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionWorkflowNotFound, "chat workflow not found")
			return
		}
		var vf *workflowcomposition.ComposeValidationFailed
		if errors.As(err, &vf) {
			s.responses.FailDetails(w, wire.ApiErrorCodeWorkflowValidationFailed, map[string]any{"errors": vf.Errors}, "workflow validation failed")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	s.Sessions.Catalog.InvalidateEffectiveCatalog(sess.ProjectID)
	if s.EventPublisher != nil {
		s.EventPublisher.PublishWorkflowPersisted(r.Context(), sess.ProjectID, sessionID, wire.WorkflowEvent{
			Event:      wire.WorkflowEventKindWorkflowPersisted,
			WorkflowID: result.Summary.ID,
			Status:     string(result.Summary.Scope),
			Path:       result.Path,
			Version:    result.Summary.Version,
			CreatedBy:  string(workflowdrafts.User),
		})
	}
	httpio.WriteJSON(w, http.StatusCreated, wire.PersistWorkflowResponse{
		Path:    result.Path,
		Summary: result.Summary,
	})
}
