package projectadmin

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/projectremoval"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// InitProjectRemoval builds the removal owner around this handler's delete
// path. It captures s, so call it once the handler is at its final address.
func (s *Removal) InitProjectRemoval() {
	s.Owner = &projectremoval.Owner{
		Projects: s.Registry, Store: projectremoval.NewStore(s.Database), Delete: s.deleteProject,
		Extensions: s.Extensions.Owner,
		SuggestionsApply: func(p project.Project) bool {
			return s.Settings.TrustSurfaces.Applies(projectcontrib.SurfaceExtensionSuggestions, p)
		},
	}
}

func (s *Removal) HandleAssessProjectRemoval(w http.ResponseWriter, r *http.Request) {
	result, err := s.Owner.Assess(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeRemovalLookupError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, result)
}

func (s *Removal) HandleCreateProjectRemoval(w http.ResponseWriter, r *http.Request) {
	var req wire.ProjectRemovalRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if _, err := uuid.Parse(req.OperationID); err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "The operation_id must be a UUID.")
		return
	}
	result, err := s.Owner.Remove(r.Context(), chi.URLParam(r, "id"), req)
	if err != nil {
		s.writeRemovalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusAccepted, result)
}

// HandleGetProjectRemoval reads a removal after its project is gone, so the
// project is checked only when no operation answers: an unknown project wins
// over an unknown operation.
func (s *Removal) HandleGetProjectRemoval(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "id")
	result, err := s.Owner.Result(r.Context(), projectID, chi.URLParam(r, "operation_id"))
	if errors.Is(err, projectremoval.ErrOperationNotFound) {
		if _, ok := s.Projects.requireProject(w, r, projectID); !ok {
			return
		}
	}
	if err != nil {
		s.writeRemovalLookupError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, result)
}

// writeRemovalLookupError answers a failed read of a project's removal
// assessment or operation.
func (s *Removal) writeRemovalLookupError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, projectremoval.ErrOperationNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeProjectRemovalNotFound, "Project removal operation not found.")
		return
	}
	s.responses.ProjectLookupError(w, r, err)
}

func (s *Removal) writeRemovalError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, projectremoval.ErrAssessmentChanged):
		s.responses.Fail(w, wire.ApiErrorCodeProjectRemovalAssessmentChanged, "Project removal evidence changed. Review it again.")
	case errors.Is(err, projectremoval.ErrInvalidSelection):
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "Only extensions assessed as eligible may be selected.")
	case errors.Is(err, projectremoval.ErrOperationConflict):
		s.responses.Fail(w, wire.ApiErrorCodeProjectRemovalOperationConflict, "This operation id was already used for another removal request.")
	case errors.Is(err, projectremoval.ErrOperationNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeProjectRemovalNotFound, "Project removal operation not found.")
	default:
		s.responses.ProjectRegistryError(w, r, err)
	}
}
