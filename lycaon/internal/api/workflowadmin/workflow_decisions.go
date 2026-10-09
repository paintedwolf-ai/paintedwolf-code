package workflowadmin

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/secretview"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
	"net/http"
	"strings"
)

func (s *Handler) HandleResolveWorkflowDecision(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")
	phaseID := chi.URLParam(r, "phase_id")
	currentRun, err := s.Workflows.Store.Runs.Get(r.Context(), runID)
	if err != nil {
		s.WriteWorkflowError(w, r, err)
		return
	}
	sessionID := currentRun.SessionID
	var req wire.ResolveWorkflowDecisionRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.ExpectedRevision < 1 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidWorkflowRevision, "expected_revision must be positive")
		return
	}
	choices := req.Choices
	if len(choices) == 0 && strings.TrimSpace(req.Choice) != "" {
		choices = []string{req.Choice}
	}
	comment := strings.TrimSpace(req.Comment)
	comment = observability.RedactCaptureText(comment)
	secretBlock, err := secretview.ReferenceBlock(s.Store, s.ManagedSecrets, r.Context(), sessionID, req.Secrets)
	if err != nil {
		secretview.WriteError(s.responses, w, r, err)
		return
	}
	comment = strings.TrimSpace(strings.Join([]string{comment, secretBlock}, "\n"))
	mutationCtx := runstate.WithExpectedRevision(context.WithoutCancel(r.Context()), req.ExpectedRevision)
	run, err := s.Workflows.Feedback.ResolveUserDecision(mutationCtx, sessionID, runID, phaseID, choices, comment)
	if err != nil {
		s.WriteWorkflowError(w, r, err)
		return
	}
	s.SessionView.WriteWorkflowRun(w, r, http.StatusOK, run)
}

func (s *Handler) HandleResolveWorkflowFeedback(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")
	phaseID := chi.URLParam(r, "phase_id")
	currentRun, err := s.Workflows.Store.Runs.Get(r.Context(), runID)
	if err != nil {
		s.WriteWorkflowError(w, r, err)
		return
	}
	sessionID := currentRun.SessionID
	var req wire.ResolveUserFeedbackRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.ExpectedRevision < 1 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidWorkflowRevision, "expected_revision must be positive")
		return
	}
	response := strings.TrimSpace(req.Response)
	response = observability.RedactCaptureText(response)
	secretBlock, err := secretview.ReferenceBlock(s.Store, s.ManagedSecrets, r.Context(), sessionID, req.Secrets)
	if err != nil {
		secretview.WriteError(s.responses, w, r, err)
		return
	}
	response = strings.TrimSpace(strings.Join([]string{response, secretBlock}, "\n"))
	mutationCtx := runstate.WithExpectedRevision(context.WithoutCancel(r.Context()), req.ExpectedRevision)
	run, err := s.Workflows.Feedback.ResolveUserFeedback(mutationCtx, sessionID, runID, phaseID, response)
	if err != nil {
		s.WriteWorkflowError(w, r, err)
		return
	}
	s.SessionView.WriteWorkflowRun(w, r, http.StatusOK, run)
}

func (s *Handler) HandleResolveWorkflowSecret(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")
	phaseID := chi.URLParam(r, "phase_id")
	currentRun, err := s.Workflows.Store.Runs.Get(r.Context(), runID)
	if err != nil {
		s.WriteWorkflowError(w, r, err)
		return
	}
	sessionID := currentRun.SessionID
	var req wire.ResolveUserSecretRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.ExpectedRevision < 1 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidWorkflowRevision, "expected_revision must be positive")
		return
	}
	mutationCtx := runstate.WithExpectedRevision(context.WithoutCancel(r.Context()), req.ExpectedRevision)
	run, err := s.Workflows.Asks.ResolveUserSecret(mutationCtx, sessionID, runID, phaseID, req.SecretValue)
	if err != nil {
		s.WriteWorkflowError(w, r, err)
		return
	}
	s.SessionView.WriteWorkflowRun(w, r, http.StatusOK, run)
}
