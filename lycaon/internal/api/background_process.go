package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Server) requireBackgroundProcessSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if !requestscope.SessionExists(s.sessionStore, &s.responses, w, r, id) {
		return "", false
	}
	return id, true
}

// handleListBackgroundProcesses provides process state for client rehydration.
func (s *Server) handleListBackgroundProcesses(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireBackgroundProcessSession(w, r)
	if !ok {
		return
	}
	processes := s.sessions.ListBackgroundProcesses(r.Context(), id)
	if processes == nil {
		processes = []wire.BackgroundProcess{}
	}
	httpio.WriteJSON(w, http.StatusOK, wire.BackgroundProcessListResponse{Processes: processes})
}

// handleGetBackgroundProcessOutput reads retained output without changing the process.
func (s *Server) handleGetBackgroundProcessOutput(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireBackgroundProcessSession(w, r)
	if !ok {
		return
	}
	processID := strings.TrimSpace(chi.URLParam(r, "process_id"))
	if processID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "process_id required")
		return
	}
	output, err := s.sessions.GetBackgroundProcessOutput(r.Context(), id, processID)
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeBackgroundProcessNotFound, "background process not found")
		return
	}
	httpio.WriteJSON(w, http.StatusOK, output)
}

// handleStopBackgroundProcess uses the same process registry as the command_stop tool.
func (s *Server) handleStopBackgroundProcess(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireBackgroundProcessSession(w, r)
	if !ok {
		return
	}
	processID := strings.TrimSpace(chi.URLParam(r, "process_id"))
	if processID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "process_id required")
		return
	}
	result, err := s.sessions.StopBackgroundProcess(id, processID)
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeBackgroundProcessNotFound, "background process not found")
		return
	}
	httpio.WriteJSON(w, http.StatusOK, result)
}
