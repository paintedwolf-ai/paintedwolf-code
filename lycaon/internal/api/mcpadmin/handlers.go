// Package mcpadmin serves MCP provider configuration, OAuth, and provider notices.
package mcpadmin

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Handler serves MCP provider settings over the device registry.
type Handler struct {
	catalog        *mcp.ProviderCatalog
	administration *mcp.ProviderAdministration
	credentials    *mcp.ProviderCredentials
	discovery      *mcp.ToolDiscovery
	projects       project.Registry
	responses      *httpio.Responder
}

func New(registry *mcp.Runtime, projects project.Registry, responses *httpio.Responder) *Handler {
	httpio.RequireDependencies("mcpadmin",
		httpio.Required{Name: "responses", Present: responses != nil},
		httpio.Required{Name: "registry", Present: registry != nil},
		httpio.Required{Name: "projects", Present: projects != nil},
	)
	return &Handler{catalog: registry.Catalog, administration: registry.Administration, credentials: registry.Credentials, discovery: registry.Tools, projects: projects, responses: responses}
}

// mcpScope reads project_id from the request. Scope is per-call, not registry state.
func (s *Handler) mcpScope(w http.ResponseWriter, r *http.Request) (mcp.CallScope, bool) {
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		return mcp.CallScope{}, true
	}
	p, ok := requestscope.ProjectByID(s.projects, s.responses, w, r, projectID)
	if !ok {
		return mcp.CallScope{}, false
	}
	return mcp.ProjectScope(projectID, project.PrimaryRootPath(p), project.RootPaths(p)), true
}

func (s *Handler) ListProviders(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.mcpScope(w, r)
	if !ok {
		return
	}
	providers := s.decorateMCPProviders(s.catalog.ListProviders(r.Context(), scope))
	if providers == nil {
		providers = []wire.McpProvider{}
	}
	httpio.WriteJSON(w, http.StatusOK, wire.McpProviderListResponse{Providers: providers})
}

func (s *Handler) CreateProvider(w http.ResponseWriter, r *http.Request) {
	var req wire.CreateMcpProviderRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	scope, ok := s.mcpScope(w, r)
	if !ok {
		return
	}
	row, err := s.administration.CreateProvider(r.Context(), scope, req, scope.ProjectDir)
	if err != nil {
		s.writeMCPAdminError(w, err)
		return
	}
	httpio.WriteJSON(w, http.StatusCreated, s.decorateMCPProvider(row))
}

func (s *Handler) ListRecipes(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.mcpScope(w, r)
	if !ok {
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.McpRecipeCatalogResponse{
		Recipes: s.administration.ListRecipes(r.Context(), scope),
	})
}

func (s *Handler) UpdateProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "provider_id")
	var req wire.UpdateMcpProviderRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	scope, ok := s.mcpScope(w, r)
	if !ok {
		return
	}
	row, err := s.administration.UpdateProvider(r.Context(), scope, id, req, scope.ProjectDir)
	if err != nil {
		s.writeMCPAdminError(w, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.decorateMCPProvider(row))
}

func (s *Handler) DeleteProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "provider_id")
	scope, ok := s.mcpScope(w, r)
	if !ok {
		return
	}
	if err := s.administration.DeleteOverlay(r.Context(), id, scope.ProjectDir); err != nil {
		s.writeMCPAdminError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Handler) ListProviderTools(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "provider_id")
	scope, ok := s.mcpScope(w, r)
	if !ok {
		return
	}
	tools, err := s.administration.ListDiscoveredTools(r.Context(), scope, id)
	if err != nil {
		s.writeMCPAdminError(w, err)
		return
	}
	if tools == nil {
		tools = []wire.McpToolInfo{}
	}
	httpio.WriteJSON(w, http.StatusOK, wire.McpToolListResponse{Tools: tools})
}

func (s *Handler) RefreshProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "provider_id")
	scope, ok := s.mcpScope(w, r)
	if !ok {
		return
	}
	row, err := s.administration.ResyncProvider(r.Context(), scope, id)
	if err != nil {
		s.writeMCPAdminError(w, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.decorateMCPProvider(row))
}

func (s *Handler) CheckProviders(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.mcpScope(w, r)
	if !ok {
		return
	}
	rows := s.decorateMCPCheckRows(s.administration.Check(r.Context(), scope))
	if rows == nil {
		rows = []wire.McpCheckRow{}
	}
	httpio.WriteJSON(w, http.StatusOK, wire.McpCheckResponse{Providers: rows})
}

func (s *Handler) StartOAuth(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.mcpScope(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "provider_id")
	row, err := s.credentials.StartOAuth(r.Context(), scope, id)
	if err != nil {
		s.writeMCPAdminError(w, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, row)
}

func (s *Handler) CompleteOAuth(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "provider_id")
	var req wire.McpOAuthCompleteRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if strings.TrimSpace(req.Code) == "" || strings.TrimSpace(req.State) == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "code and state are required")
		return
	}
	scope, ok := s.mcpScope(w, r)
	if !ok {
		return
	}
	row, err := s.credentials.CompleteOAuth(r.Context(), scope, id, req)
	if err != nil {
		s.writeMCPAdminError(w, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.decorateMCPProvider(row))
}

func (s *Handler) CancelOAuth(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.mcpScope(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "provider_id")
	var req wire.McpOAuthCancelRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if strings.TrimSpace(req.State) == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "state is required")
		return
	}
	if err := s.credentials.CancelOAuth(r.Context(), scope, id, req.State); err != nil {
		s.writeMCPAdminError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Handler) RevokeOAuth(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "provider_id")
	scope, ok := s.mcpScope(w, r)
	if !ok {
		return
	}
	if _, err := s.credentials.RevokeOAuth(r.Context(), scope, id); err != nil {
		s.writeMCPAdminError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
