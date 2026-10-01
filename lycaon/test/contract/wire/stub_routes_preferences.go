package contract

import (
	"net/http"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/pkg/api"
)

func registerStubPreferenceRoutes(mux *http.ServeMux, writeJSON stubJSONWriter) {
	reviewFixture := api.ReviewSettingsResponse{
		Scope:       api.SettingsScopeGlobal,
		ReviewPaths: []api.ContentReviewRule{},
		MergedFrom:  []string{"bundled"},
	}
	mux.HandleFunc("GET /v1/settings/review", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, reviewFixture)
	})
	mux.HandleFunc("PATCH /v1/settings/review", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, reviewFixture)
	})
	verifyFixture := api.VerifySettingsResponse{
		Scope:             api.SettingsScopeProject,
		Test:              "./task check",
		VerifyPath:        settingsoverlay.Rel("verify.yaml"),
		BackendConfigured: true,
	}
	mux.HandleFunc("GET /v1/settings/verify", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, verifyFixture)
	})
	mux.HandleFunc("PATCH /v1/settings/verify", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, verifyFixture)
	})
	mux.HandleFunc("POST /v1/settings/verify/dismiss", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, verifyFixture)
	})
	securityScansFixture := api.SecurityScannersSettingsResponse{
		Enabled:           true,
		MergedFrom:        []string{"bundled"},
		LandedChangeScope: api.LandedChangeScopePathScoped,
		SourceVerify:      api.SourceVerifyStat,
	}
	mux.HandleFunc("GET /v1/settings/security-scanners", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, securityScansFixture)
	})
	mux.HandleFunc("PATCH /v1/settings/security-scanners", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, securityScansFixture)
	})
	trustSettingsFixture := api.TrustSettingsResponse{
		Surfaces: []api.TrustSurfaceSetting{
			{ID: api.TrustSurfaceAgentsMD, Label: "Instructions", Group: api.TrustGroupSteering, Enabled: true},
			{ID: api.TrustSurfaceSkills, Label: "Skills", Group: api.TrustGroupSteering, Enabled: true},
			{ID: api.TrustSurfaceProjectSettings, Label: "Approvals & limits", Group: api.TrustGroupSteering, Enabled: true},
			{ID: api.TrustSurfaceProjectMCP, Label: "MCP providers", Group: api.TrustGroupSteering, Enabled: true},
			{ID: api.TrustSurfaceScanConfig, Label: "Scan reporting", Group: api.TrustGroupReplacesDefaults, Enabled: true},
			{ID: api.TrustSurfacePromptOverrides, Label: "App prompts", Group: api.TrustGroupReplacesDefaults, Enabled: true},
			{ID: api.TrustSurfaceExtensionConfig, Label: "Extension settings", Group: api.TrustGroupReplacesDefaults, Enabled: true},
			{ID: api.TrustSurfaceExtensionSuggestions, Label: "Suggested extensions", Group: api.TrustGroupSuggestion, Enabled: true},
		},
	}
	mux.HandleFunc("GET /v1/settings/project-trust", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, trustSettingsFixture)
	})
	mux.HandleFunc("PATCH /v1/settings/project-trust", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, trustSettingsFixture)
	})
}
