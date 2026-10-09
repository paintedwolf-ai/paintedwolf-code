package workflowadmin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowblueprints "github.com/lycaon/lycaon/internal/workflow/blueprints"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *BlueprintRoutes) HandleCreateBlueprint(w http.ResponseWriter, r *http.Request) {
	var req wire.CreateBlueprintRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	projectID := chi.URLParam(r, "id")
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "title"}, "title is required")
		return
	}
	if _, ok := requestscope.ProjectByID(s.Projects, s.responses, w, r, projectID); !ok {
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	out, err := s.Blueprints.Create(r.Context(), projectID, req.Title, req.Path, req.SourceWorkflowID, "")
	if err != nil {
		if errors.Is(err, blueprint.ErrInvalidBlueprintPath) {
			s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest,
				map[string]any{"field": "path", "reason": "must be a blueprint path inside the project"},
				"path must be a blueprint path inside the project")
			return
		}
		if errors.Is(err, blueprint.ErrPathTaken) {
			s.responses.Fail(w, wire.ApiErrorCodeBlueprintPathTaken, "a blueprint already uses this path")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusCreated, out)
}

func (s *BlueprintRoutes) HandleListBlueprints(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "id")
	if _, ok := requestscope.ProjectByID(s.Projects, s.responses, w, r, projectID); !ok {
		return
	}
	if path, filtered := r.URL.Query()["path"]; filtered {
		s.writeBlueprintAtPath(w, r, projectID, path)
		return
	}
	out, truncated, err := s.Blueprints.List(r.Context(), projectID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	s.attachCompatibleWorkflows(r.Context(), projectID, out)
	httpio.WriteJSON(w, http.StatusOK, wire.BlueprintListResponse{Blueprints: out, Truncated: truncated})
}

// writeBlueprintAtPath answers the list filtered to one convention path.
func (s *BlueprintRoutes) writeBlueprintAtPath(w http.ResponseWriter, r *http.Request, projectID string, values []string) {
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		s.responses.InvalidQueryParam(w, "path", "must be one project-relative blueprint path")
		return
	}
	out := []wire.BlueprintSummary{}
	bp, err := s.Blueprints.Get(r.Context(), projectID, values[0])
	switch {
	case errors.Is(err, blueprint.ErrNotFound):
	case err != nil:
		s.responses.InternalError(w, r, err)
		return
	default:
		out = append(out, wire.BlueprintSummary{
			ID:               bp.ID,
			Title:            bp.Title,
			Path:             bp.Path,
			SourceWorkflowID: bp.SourceWorkflowID,
			Status:           bp.Status,
			Version:          bp.Version,
			UpdatedAt:        bp.UpdatedAt,
		})
		s.attachCompatibleWorkflows(r.Context(), projectID, out)
	}
	httpio.WriteJSON(w, http.StatusOK, wire.BlueprintListResponse{Blueprints: out})
}

// blueprintAddress resolves {id}/{blueprint_id} to the blueprint's project and
// convention path, writing the lookup error when either names nothing.
func (s *BlueprintRoutes) blueprintAddress(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	projectID := chi.URLParam(r, "id")
	if _, ok := requestscope.ProjectByID(s.Projects, s.responses, w, r, projectID); !ok {
		return "", "", false
	}
	path, err := s.Blueprints.PathForID(r.Context(), projectID, chi.URLParam(r, "blueprint_id"))
	if err != nil {
		if errors.Is(err, blueprint.ErrNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeBlueprintNotFound, "blueprint not found")
			return "", "", false
		}
		s.responses.InternalError(w, r, err)
		return "", "", false
	}
	return projectID, path, true
}

func (s *BlueprintRoutes) HandleGetBlueprint(w http.ResponseWriter, r *http.Request) {
	projectID, path, ok := s.blueprintAddress(w, r)
	if !ok {
		return
	}
	out, err := s.Blueprints.Get(r.Context(), projectID, path)
	if err != nil {
		if errors.Is(err, blueprint.ErrNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeBlueprintNotFound, "blueprint not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	if out != nil {
		out.CompatibleWorkflows = s.compatibleWorkflowsFor(r.Context(), out.ProjectID, out.Path)
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

func (s *BlueprintRoutes) HandleUpdateBlueprint(w http.ResponseWriter, r *http.Request) {
	projectID, path, ok := s.blueprintAddress(w, r)
	if !ok {
		return
	}
	var req wire.UpdateBlueprintRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.Content == nil && req.Title == nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "content or title is required")
		return
	}
	if req.Title != nil {
		t, err := blueprint.NormalizeBlueprintDisplayTitle(*req.Title)
		if err != nil {
			s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "title must be a non-empty display name without control characters")
			return
		}
		req.Title = &t
	}
	out, err := s.Blueprints.Update(r.Context(), projectID, path, req.Content, req.Title)
	if err != nil {
		if errors.Is(err, blueprint.ErrNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeBlueprintNotFound, "blueprint not found")
			return
		}
		if errors.Is(err, blueprint.ErrInvalidStatus) {
			s.responses.Fail(w, wire.ApiErrorCodeBlueprintNotEditable, "blueprint is not editable in its current status")
			return
		}
		if errors.Is(err, blueprint.ErrContentChanged) {
			s.responses.Fail(w, wire.ApiErrorCodeBlueprintContentConflict, "blueprint changed; reload it")
			return
		}
		if errors.Is(err, blueprint.ErrInvalidBlueprintTitle) {
			s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "title must be a non-empty display name without control characters")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, out)
	if out != nil {
		s.Workflows.Blueprints.NotifyBlueprintPathWritten(r.Context(), "", projectID, out.Path)
	}
}

func (s *BlueprintRoutes) HandleDeleteBlueprint(w http.ResponseWriter, r *http.Request) {
	projectID, path, ok := s.blueprintAddress(w, r)
	if !ok {
		return
	}
	if err := s.Blueprints.Delete(r.Context(), projectID, path); err != nil {
		if errors.Is(err, blueprint.ErrNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeBlueprintNotFound, "blueprint not found")
			return
		}
		if errors.Is(err, blueprint.ErrRunActive) {
			s.responses.Fail(w, wire.ApiErrorCodeBlueprintRunActive, "a workflow run is using this blueprint")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *BlueprintRoutes) attachCompatibleWorkflows(ctx context.Context, projectID string, rows []wire.BlueprintSummary) {
	if len(rows) == 0 {
		return
	}
	compatCache := map[string][]string{}
	for i := range rows {
		path := rows[i].Path
		if cached, ok := compatCache[path]; ok {
			rows[i].CompatibleWorkflows = cached
			continue
		}
		ids := s.compatibleWorkflowsFor(ctx, projectID, path)
		compatCache[path] = ids
		rows[i].CompatibleWorkflows = ids
	}
}

func (s *BlueprintRoutes) compatibleWorkflowsFor(ctx context.Context, projectID, path string) []string {
	projectDir := ""
	if p, err := s.Projects.Get(ctx, projectID); err == nil {
		projectDir = project.PrimaryRootPath(p)
	}
	manifests, err := s.blueprintCatalogManifests(ctx, projectDir)
	if err != nil {
		return nil
	}
	return workflowblueprints.CompatibleWorkflowIDs(path, manifests)
}

func (s *BlueprintRoutes) blueprintCatalogManifests(ctx context.Context, projectDir string) ([]workflowdef.Manifest, error) {
	reg, _, err := s.Catalog.Resolve(ctx, projectDir, "")
	if err != nil {
		return nil, err
	}
	return reg.List(), nil
}
