package projectadmin

import (
	"context"
	"errors"
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/projectcontrol"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// requireProject loads the project a route addresses; an unknown id answers
// project_not_found before any child lookup or mutation gate.
func (s *Projects) requireProject(w http.ResponseWriter, r *http.Request, projectID string) (*project.Project, bool) {
	p, err := s.Registry.Get(r.Context(), projectID)
	if err != nil {
		s.responses.ProjectLookupError(w, r, err)
		return nil, false
	}
	return p, true
}

func (s *Projects) beginProjectMutation(w http.ResponseWriter, r *http.Request, projectID string) func() {
	if err := s.MutationGate.BeginMutation(projectID); err != nil {
		s.writeMutationGateError(w, r, err)
		return nil
	}
	return func() { s.MutationGate.EndMutation(projectID) }
}

func (s *Projects) beginDrainingProjectMutation(w http.ResponseWriter, r *http.Request, projectID string) (func(), func(context.Context) error) {
	wait, err := s.MutationGate.BeginDrainingMutation(projectID)
	if err != nil {
		s.writeMutationGateError(w, r, err)
		return nil, nil
	}
	return func() { s.MutationGate.EndMutation(projectID) }, wait
}

func (s *Projects) writeMutationGateError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, project.ErrMutationInProgress):
		s.responses.Fail(w, wire.ApiErrorCodeProjectMutationInProgress, "this project is already changing")
	case errors.Is(err, project.ErrProjectBusy):
		s.responses.Fail(w, wire.ApiErrorCodeProjectBusy, "project has in-flight work")
	default:
		s.responses.InternalError(w, r, err)
	}
}

func (s *Projects) queryForce(w http.ResponseWriter, r *http.Request) (bool, bool) {
	force, _, err := httpio.OptionalBoolQuery(r, "force")
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return false, false
	}
	return force, true
}

func (s *Projects) writeRootBusy(w http.ResponseWriter, dependents projectcontrol.RootDependents) {
	details := dependents.Details()

	s.responses.FailDetails(w, wire.ApiErrorCodeRootBusy, details, "folder has in-flight dependents")
}
