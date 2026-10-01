package contract

import (
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

func registerStubIntegrationRoutes(mux *http.ServeMux, writeJSON stubJSONWriter) {
	registerStubHistoryStorageRoutes(mux, writeJSON)
	mux.HandleFunc("GET /v1/mcp/recipes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.McpRecipeCatalogResponse{Recipes: []api.McpRecipe{}})
	})
	mux.HandleFunc("GET /v1/mcp/providers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.McpProviderListResponse{Providers: []api.McpProvider{{ID: "filesystem", Enabled: false, Class: api.McpProviderClassLocal, ToolLoading: api.McpToolLoadingAuto}}})
	})
	mux.HandleFunc("POST /v1/mcp/providers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.McpProvider{ID: "filesystem", Enabled: false, Class: api.McpProviderClassLocal, ToolLoading: api.McpToolLoadingAuto, Status: api.McpStatusDisabled})
	})
	mux.HandleFunc("PATCH /v1/mcp/providers/{provider_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.McpProvider{ID: "filesystem", Enabled: true, Class: api.McpProviderClassLocal, ToolLoading: api.McpToolLoadingAuto})
	})
	mux.HandleFunc("DELETE /v1/mcp/providers/{provider_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/mcp/providers/{provider_id}/tools", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.McpToolListResponse{Tools: []api.McpToolInfo{{Name: "mcp_filesystem_list", Description: "list"}}})
	})
	mux.HandleFunc("POST /v1/mcp/providers/{provider_id}/refresh", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.McpProvider{ID: "filesystem", Enabled: true, Class: api.McpProviderClassLocal, ToolLoading: api.McpToolLoadingAuto, Status: api.McpStatusReady})
	})
	mux.HandleFunc("POST /v1/mcp/providers/{provider_id}/oauth/start", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.McpOAuthStartResponse{
			AuthorizeURL: "https://auth.example/authorize",
			State:        "stub-state",
			RedirectURI:  "http://127.0.0.1:8766/mcp/oauth/callback",
		})
	})
	mux.HandleFunc("POST /v1/mcp/providers/{provider_id}/oauth/cancel", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/mcp/providers/{provider_id}/oauth/complete", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.McpProvider{ID: "filesystem", Enabled: true, Class: api.McpProviderClassLocal, ToolLoading: api.McpToolLoadingAuto, Status: api.McpStatusReady, SignedIn: true})
	})
	mux.HandleFunc("DELETE /v1/mcp/providers/{provider_id}/oauth", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/mcp/providers/check", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.McpCheckResponse{Providers: []api.McpCheckRow{{ProviderID: "filesystem", Status: api.McpCheckRowStatusHealthy}}})
	})
	mux.HandleFunc("GET /v1/web-research/providers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, stubWebResearchProvidersResponse())
	})
	mux.HandleFunc("GET /v1/web-research/index", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.WebResearchIndexStatus{
			Warming:  true,
			Activity: []api.WebResearchWarmActivity{},
		})
	})
	mux.HandleFunc("GET /v1/local-data", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.LocalDataStatus{
			Buckets: []api.LocalDataBucketStatus{
				{ID: api.LocalDataBucketWebIndex, Present: false},
			},
			WorkspaceCaches: []api.WorkspaceCacheStatus{},
		})
	})
	mux.HandleFunc("POST /v1/local-data/clear", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.LocalDataClearResponse{
			Results: []api.LocalDataClearResult{
				{ID: api.LocalDataBucketWebIndex, OK: true},
			},
			WorkspaceCacheResults: []api.WorkspaceCacheClearResult{},
		})
	})
	mux.HandleFunc("GET /v1/backup/capabilities", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.BackupCapabilities{
			FormatVersion: 1, SchemaRevision: 1,
			MaxArchiveBytes: 1 << 30, MaxExpandedBytes: 4 << 30,
		})
	})
	mux.HandleFunc("GET /v1/backup", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("PK\x05\x06" + strings.Repeat("\x00", 18)))
	})
	mux.HandleFunc("POST /v1/backup/restore", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.BackupRestoreResult{
			RestartRequired:  true,
			RecoveryCopyPath: "/tmp/restore-recovery",
		})
	})
	mux.HandleFunc("POST /v1/backup/restore/recovery-snapshot", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.BackupRestoreResult{
			RestartRequired:  true,
			RecoveryCopyPath: "/tmp/restore-recovery",
		})
	})
	mux.HandleFunc("POST /v1/store/reset", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.BackupRestoreResult{
			RestartRequired:  true,
			RecoveryCopyPath: "/tmp/fresh-start-recovery",
		})
	})
	mux.HandleFunc("GET /v1/host-resources", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, stubHostResourcesResponse())
	})
	mux.HandleFunc("POST /v1/host-resources/refresh", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, stubHostResourcesResponse())
	})
	mux.HandleFunc("PATCH /v1/host-resources/{resource_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, stubHostResourcesResponse())
	})
}
