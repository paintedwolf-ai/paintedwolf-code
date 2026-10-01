package contract

import (
	"net/http"
	"strings"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func registerStubSettingsWorkflowRoutes(mux *http.ServeMux, now time.Time, writeJSON stubJSONWriter) {
	registerStubPolicyApprovalRoutes(mux, writeJSON)
	registerStubPreferenceRoutes(mux, writeJSON)
	registerStubWorkflowRoutes(mux, now, writeJSON)
	registerStubGitRoutes(mux, writeJSON)

	pricingFixture := api.SettingsPricingResponse{
		CostTrackingEnabled: true,
		Sources:             []api.SettingsPricingSource{{ID: "openrouter", Enabled: true}},
		AvailableSources: []api.PricingSourceMeta{{
			ID: "openrouter", Label: "OpenRouter", Kind: "http", Enabled: true,
			Status: api.PricingSourceStatusOK, ModelCount: 3,
		}},
	}
	mux.HandleFunc("GET /v1/settings/pricing", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, pricingFixture)
	})
	mux.HandleFunc("PATCH /v1/settings/pricing", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, pricingFixture)
	})
	mux.HandleFunc("GET /v1/settings/file-summaries", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.FileSummariesSettingsResponse{Enabled: true})
	})
	mux.HandleFunc("PATCH /v1/settings/file-summaries", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.FileSummariesSettingsResponse{Enabled: true})
	})
	mux.HandleFunc("GET /v1/settings/power", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.PowerSettingsResponse{
			KeepAwakeWhileWorking: true,
			Supported:             true,
			Inhibiting:            false,
			ActiveWorkCount:       0,
		})
	})
	mux.HandleFunc("PATCH /v1/settings/power", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.PowerSettingsResponse{
			KeepAwakeWhileWorking: true,
			Supported:             true,
			Inhibiting:            false,
			ActiveWorkCount:       0,
		})
	})
	mux.HandleFunc("POST /v1/settings/pricing/sources/{id}/refresh", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.PricingSourceMeta{
			ID: "openrouter", Label: "OpenRouter", Kind: "http", Enabled: true,
			Status: api.PricingSourceStatusOK, ModelCount: 3,
		})
	})
	mux.HandleFunc("GET /v1/attention", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.AttentionView{Rows: []api.AttentionRow{}})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/previews/watch", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.PreviewWatchResult{Watching: true, PageID: "page-1"})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/previews", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.PreviewListResponse{Previews: []api.PreviewAttachment{}})
	})
	mux.HandleFunc("GET /v1/settings/web-research", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, stubWebResearchSettings())
	})
	mux.HandleFunc("PATCH /v1/settings/web-research", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, stubWebResearchSettings())
	})
	mux.HandleFunc("PATCH /v1/web-research/providers/{provider_id}", func(w http.ResponseWriter, r *http.Request) {
		id := api.WebSearchProvider(r.PathValue("provider_id"))
		if strings.HasPrefix(string(id), "{") || id == "" {
			id = api.WebSearchProviderBrave
		}
		writeJSON(w, http.StatusOK, api.WebResearchProviderMeta{
			ID:                id,
			Kind:              api.WebResearchProviderKindKeyed,
			Label:             "Brave Search",
			Roles:             []api.WebResearchProviderRole{api.WebResearchProviderRoleResults},
			DefaultEnabled:    true,
			Configured:        true,
			CredentialPresent: true,
		})
	})
	mux.HandleFunc("PUT /v1/web-research/providers/{provider_id}/credential", func(w http.ResponseWriter, r *http.Request) {
		id := api.WebSearchProvider(r.PathValue("provider_id"))
		if strings.HasPrefix(string(id), "{") || id == "" {
			id = api.WebSearchProviderBrave
		}
		writeJSON(w, http.StatusOK, api.WebResearchProviderMeta{
			ID:                id,
			Kind:              api.WebResearchProviderKindKeyed,
			Label:             "Brave Search",
			Roles:             []api.WebResearchProviderRole{api.WebResearchProviderRoleResults},
			DefaultEnabled:    true,
			Configured:        true,
			CredentialPresent: true,
		})
	})
	mux.HandleFunc("DELETE /v1/web-research/providers/{provider_id}/credential", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/web-research/providers/{provider_id}/test", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ProviderProbeResult{OK: true})
	})
	mux.HandleFunc("GET /v1/providers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ProviderListResponse{
			Providers: []api.ProviderMeta{{
				ID:                "anthropic",
				Configured:        true,
				CredentialPresent: false,
				Features:          stubProviderFeatures("explicit_breakpoints"),
				Models:            []api.ProviderModelMeta{stubProviderModel("claude")},
			}},
		})
	})
	mux.HandleFunc("POST /v1/providers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.ProviderMeta{
			ID:                "custom",
			BaseURL:           "https://example.com/v1",
			Configured:        false,
			CredentialPresent: false,
			Features:          stubProviderFeatures("none"),
			Models:            []api.ProviderModelMeta{stubProviderModel("custom")},
		})
	})
	mux.HandleFunc("GET /v1/provider-kinds", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ProviderKindListResponse{
			Kinds: []api.ProviderKindTemplate{{
				Kind:           "anthropic",
				Label:          "Anthropic",
				BaseURL:        "https://api.anthropic.com/v1",
				RequiresAPIKey: true,
			}},
		})
	})
	mux.HandleFunc("PATCH /v1/providers/{provider_id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("provider_id")
		if strings.HasPrefix(id, "{") || id == "" {
			id = "custom"
		}
		writeJSON(w, http.StatusOK, api.ProviderMeta{
			ID:                id,
			BaseURL:           "https://example.com/v1",
			Configured:        false,
			CredentialPresent: false,
			Features:          stubProviderFeatures("none"),
			Models:            []api.ProviderModelMeta{stubProviderModel("custom")},
		})
	})
	mux.HandleFunc("DELETE /v1/providers/{provider_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /v1/providers/{provider_id}/credential", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("provider_id")
		if strings.HasPrefix(id, "{") || id == "" {
			id = "custom"
		}
		writeJSON(w, http.StatusOK, api.ProviderMeta{
			ID:                id,
			BaseURL:           "https://example.com/v1",
			Configured:        true,
			CredentialPresent: true,
			Features:          stubProviderFeatures("none"),
			Models:            []api.ProviderModelMeta{stubProviderModel("custom")},
		})
	})
	mux.HandleFunc("DELETE /v1/providers/{provider_id}/credential", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/providers/{provider_id}/test", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ProviderProbeResult{OK: true})
	})
	mux.HandleFunc("POST /v1/providers/{provider_id}/refresh", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("provider_id")
		if strings.HasPrefix(id, "{") || id == "" {
			id = "custom"
		}
		writeJSON(w, http.StatusOK, api.ProviderMeta{
			ID:                id,
			BaseURL:           "https://example.com/v1",
			Configured:        true,
			CredentialPresent: false,
			Features:          stubProviderFeatures("none"),
			Models:            []api.ProviderModelMeta{stubProviderModel("custom")},
		})
	})
}
