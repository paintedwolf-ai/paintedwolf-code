package requestscope

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func ProjectByURLID(registry project.Registry, responses *httpio.Responder, w http.ResponseWriter, r *http.Request) (*project.Project, bool) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "id"}, "project id is required")
		return nil, false
	}
	p, err := registry.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			responses.Fail(w, wire.ApiErrorCodeProjectNotFound, "project not found")
			return nil, false
		}
		responses.InternalError(w, r, err)
		return nil, false
	}
	return p, true
}

func ProjectByID(registry project.Registry, responses *httpio.Responder, w http.ResponseWriter, r *http.Request, projectID string) (*project.Project, bool) {
	p, err := registry.Get(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			responses.Fail(w, wire.ApiErrorCodeProjectNotFound, "project not found")
			return nil, false
		}
		responses.InternalError(w, r, err)
		return nil, false
	}
	return p, true
}

// ProjectIDQuery resolves a canonical project UUID and returns its primary root path.
func ProjectIDQuery(registry project.Registry, responses *httpio.Responder, w http.ResponseWriter, r *http.Request) (projectID, projectPath string, ok bool) {
	projectID = strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		responses.FailDetails(w, wire.ApiErrorCodeInvalidQuery, map[string]any{"param": "project_id"}, "project_id is required")
		return "", "", false
	}
	p, err := registry.Get(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			responses.Fail(w, wire.ApiErrorCodeProjectNotFound, "project not found")
			return "", "", false
		}
		responses.InternalError(w, r, err)
		return "", "", false
	}
	overlay, err := project.ResolveProjectOverlay(p, "")
	if err != nil {
		responses.InternalError(w, r, err)
		return "", "", false
	}
	if err := overlay.CheckCompatibility(); err != nil {
		if !responses.OverlayFormatError(w, err) {
			responses.InternalError(w, r, err)
		}
		return "", "", false
	}
	return p.ID, overlay.Primary.Path, true
}

// ProjectByRootPath resolves an existing folder to its most recent project.
func ProjectByRootPath(registry project.Registry, ctx context.Context, resolved string) *project.Project {
	if registry == nil {
		return nil
	}
	list, err := registry.List(ctx)
	if err != nil {
		return nil
	}
	return project.FindByRootPath(list, resolved)
}

// GatedProjectDir returns projectDir when its trust surface applies.
func GatedProjectDir(settingsService *settings.Service, p *project.Project, projectDir, surface string) string {
	if !ProjectSurfaceApplies(settingsService, p, surface) {
		return ""
	}
	return projectDir
}

func ProjectSurfaceApplies(settingsService *settings.Service, p *project.Project, surface string) bool {
	if p == nil || settingsService == nil || settingsService.TrustSurfaces == nil {
		return false
	}
	return settingsService.TrustSurfaces.Applies(surface, *p)
}

// BeginRuntime admits request work on projectID while no project mutation
// runs. A nil release means the response is written.
func BeginRuntime(gate *project.MutationGate, responses *httpio.Responder, w http.ResponseWriter, r *http.Request, projectID string) func() {
	release, err := gate.BeginRuntime(projectID)
	if err != nil {
		if errors.Is(err, project.ErrMutationInProgress) {
			responses.Fail(w, wire.ApiErrorCodeProjectMutationInProgress, "this project is already changing")
		} else {
			responses.InternalError(w, r, err)
		}
		return nil
	}
	return release
}
