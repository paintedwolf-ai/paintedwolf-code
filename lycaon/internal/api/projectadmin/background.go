package projectadmin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// HandleListProjectBackgroundProcesses returns running and recently ended background commands for a project.
func (s *Projects) HandleListProjectBackgroundProcesses(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := s.requireProject(w, r, id); !ok {
		return
	}
	resp, err := s.Sessions.ProjectControl.ListProjectBackground(r.Context(), id)
	if err != nil {
		s.responses.ProjectLookupError(w, r, err)
		return
	}
	if resp.Processes == nil {
		resp.Processes = []wire.ProjectBackgroundProcess{}
	}
	httpio.WriteJSON(w, http.StatusOK, resp)
}

// HandleStopProjectBackgroundProcess stops a background command belonging to the project.
func (s *Projects) HandleStopProjectBackgroundProcess(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := s.requireProject(w, r, id); !ok {
		return
	}
	processID := strings.TrimSpace(chi.URLParam(r, "process_id"))
	if processID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "process_id required")
		return
	}
	result, err := s.Sessions.ProjectControl.StopProjectBackgroundProcess(r.Context(), id, processID)
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			s.responses.ProjectLookupError(w, r, err)
			return
		}
		s.responses.Fail(w, wire.ApiErrorCodeBackgroundProcessNotFound, "background process not found")
		return
	}
	httpio.WriteJSON(w, http.StatusOK, result)
}
