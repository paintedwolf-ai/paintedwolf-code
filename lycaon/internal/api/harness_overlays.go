package api

import (
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/harnessfixture"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Server) handleHarnessOverlays(w http.ResponseWriter, r *http.Request) {
	var request harnessfixture.Request
	if err := httpio.DecodeJSON(w, r, &request); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if err := request.Setup.Validate(); err != nil {
		s.harnessRequestRejected(w, r, "the fixture setup is not valid", err)
		return
	}
	parent, ok := requestscope.Session(s.sessionStore, &s.responses, w, r, request.SessionID)
	if !ok {
		return
	}
	project, err := s.projectRegistry.Get(r.Context(), parent.ProjectID)
	if err != nil || project == nil || len(project.Roots) != 1 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "Fixture requires one attached project root")
		return
	}
	evidence, err := harnessfixture.Prepare(r.Context(), s.workers, s.sessionStore, parent, project.Roots[0], request.Setup, s.sessions.VerifyHarnessWorker)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if err := s.harnessWorkers.Install(parent.ID, request.Setup, evidence); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, evidence)
}
