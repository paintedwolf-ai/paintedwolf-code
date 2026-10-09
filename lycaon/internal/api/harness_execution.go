package api

import (
	"github.com/go-chi/chi/v5"
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/session"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// harnessRequestRejected refuses a harness request with host copy; the cause
// stays in the sidecar log the harness driver reads.
func (s *Server) harnessRequestRejected(w http.ResponseWriter, r *http.Request, message string, err error) {
	s.responses.Logger.WarnContext(r.Context(), "harness request rejected", "path", r.URL.Path, "err", err)
	s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, message)
}

func (s *Server) handleHarnessExecution(w http.ResponseWriter, r *http.Request) {
	observation, err := s.sessions.ObserveExecution(r.Context(), chi.URLParam(r, "sessionID"), chi.URLParam(r, "submissionID"))
	if err != nil {
		s.harnessRequestRejected(w, r, "the execution could not be observed", err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, observation)
}

func (s *Server) handleHarnessModelLimit(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SessionID string `json:"session_id"`
		Limit     int    `json:"limit"`
	}
	if err := httpio.DecodeJSON(w, r, &request); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	session, ok := requestscope.Session(s.sessionStore, &s.responses, w, r, request.SessionID)
	if !ok {
		return
	}
	if session.IsWorkerChild() {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "Response allowance requires a root session")
		return
	}
	if err := s.sessionStore.ConfigureModelLimit(r.Context(), request.SessionID, request.Limit); err != nil {
		s.harnessRequestRejected(w, r, "the model limit could not be configured", err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, map[string]any{"session_id": request.SessionID, "limit": request.Limit})
}

func (s *Server) handleHarnessWorkflowExecution(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	run, err := s.Workflow.Workflows.Store.Runs.Get(r.Context(), chi.URLParam(r, "runID"))
	if err != nil || run == nil || run.SessionID != sessionID {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "workflow does not belong to session")
		return
	}
	observation, err := s.sessions.ObserveExecutionTree(r.Context(), sessionID)
	if err != nil {
		s.harnessRequestRejected(w, r, "the workflow execution could not be observed", err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, session.WorkflowExecutionObservation{Run: *run, Execution: observation})
}
