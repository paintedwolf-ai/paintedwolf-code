package workflowadmin

import (
	"context"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	wire "github.com/lycaon/lycaon/pkg/api"
	"net/http"
)

func (s *Composition) HandleComposeWorkflow(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	sess, ok := requestscope.Session(s.Store, s.responses, w, r, sessionID)
	if !ok {
		return
	}
	if err := httpio.RequireRequestMediaType(r, httpio.MediaTypeYAML); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	body, err := httpio.ReadAllBody(w, r)
	if err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	dryRun, _, err := httpio.OptionalBoolQuery(r, "dry_run")
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	result, err := s.Composer.Compose(r.Context(), workflowcomposition.ComposeRequest{
		SessionID:      sessionID,
		ProjectDir:     sess.WorkspacePath,
		ManifestYAML:   body,
		SessionPosture: sess.Posture,
		CreatedBy:      workflowdrafts.User,
		DryRun:         dryRun,
	})
	if err != nil {
		var vf *workflowcomposition.ComposeValidationFailed
		if errors.As(err, &vf) {
			s.responses.FailDetails(w, wire.ApiErrorCodeWorkflowValidationFailed, map[string]any{"errors": vf.Errors}, "workflow validation failed")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	statusCode := http.StatusCreated
	if dryRun {
		statusCode = http.StatusOK
	}
	httpio.WriteJSON(w, statusCode, wire.ComposeWorkflowResponse{
		Summary:          result.Summary,
		EffectiveYAML:    result.EffectiveYAML,
		EffectiveSummary: result.EffectiveSummary,
	})
	if !dryRun {
		s.queueComposeKick(r.Context(), sessionID)
	}
}

func (s *Composition) queueComposeKick(ctx context.Context, sessionID string) {
	s.Sessions.Emit(ctx, sessionID, anchor.ComposeDone, anchor.Envelope{})
}

func (s *Composition) HandleListWorkflowTemplates(w http.ResponseWriter, r *http.Request) {
	if s.Composer.Templates == nil {
		httpio.WriteJSON(w, http.StatusOK, wire.WorkflowTemplateListResponse{Templates: []wire.WorkflowTemplateSummary{}})
		return
	}
	templates := s.Composer.Templates.List()
	if templates == nil {
		templates = []wire.WorkflowTemplateSummary{}
	}
	httpio.WriteJSON(w, http.StatusOK, wire.WorkflowTemplateListResponse{Templates: templates})
}

func (s *Composition) HandleComposeFromTemplate(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	sess, ok := requestscope.Session(s.Store, s.responses, w, r, sessionID)
	if !ok {
		return
	}
	var req wire.ComposeFromTemplateRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	dryRun, _, err := httpio.OptionalBoolQuery(r, "dry_run")
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	result, err := s.Composer.ComposeFromTemplate(r.Context(), workflowcomposition.ComposeFromTemplateRequest{
		SessionID:      sessionID,
		ProjectDir:     sess.WorkspacePath,
		TemplateID:     req.TemplateID,
		Params:         req.Params,
		SessionPosture: sess.Posture,
		CreatedBy:      workflowdrafts.User,
		DryRun:         dryRun,
	})
	if err != nil {
		var vf *workflowcomposition.ComposeValidationFailed
		if errors.As(err, &vf) {
			s.responses.FailDetails(w, wire.ApiErrorCodeWorkflowValidationFailed, map[string]any{"errors": vf.Errors}, "workflow validation failed")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	statusCode := http.StatusCreated
	if dryRun {
		statusCode = http.StatusOK
	}
	httpio.WriteJSON(w, statusCode, wire.ComposeWorkflowResponse{
		Summary:          result.Summary,
		EffectiveYAML:    result.EffectiveYAML,
		EffectiveSummary: result.EffectiveSummary,
	})
	if !dryRun {
		s.queueComposeKick(r.Context(), sessionID)
	}
}
