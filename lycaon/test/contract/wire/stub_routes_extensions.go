package contract

import (
	"net/http"

	"github.com/lycaon/lycaon/pkg/api"
)

func registerStubContributionExtensionRoutes(mux *http.ServeMux, writeJSON stubJSONWriter) {
	mux.HandleFunc("GET /v1/contributions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ContributionFrameResponse{
			FrameRevision:   "stub-frame-revision",
			Commands:        []api.ContributionCommand{},
			Menus:           []api.ContributionMenu{},
			Keybindings:     []api.ContributionKeybinding{},
			BindingDefaults: []api.ContributionBindingDefault{},
			EditorActions:   []api.ContributionEditorAction{},
			Themes:          []api.ContributionTheme{},
			Configuration:   []api.ContributionConfigurationProperty{},
			Requirements:    []api.ContributionRequirement{},
			SearchSources:   []api.ContributionSearchSource{},
			Operations:      []api.ContributionOperation{},
			Notes:           []api.ContributionNote{},
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/commands/{command_id}/choices/{step_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ContributionChoiceResponse{
			FrameRevision: "stub-frame-revision",
			Provider:      "stub-provider",
			Choices:       []api.ContributionChoice{},
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/contribution-search/{source_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ContributionSearchResponse{
			FrameRevision: "stub-frame-revision",
			SourceID:      "acme/test:stub-source-search",
			Provider:      "stub-provider",
			Results:       []api.ContributionSearchResult{},
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/commands/{command_id}/invoke", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.CommandInvokeResponse{
			Status:        "completed",
			FrameRevision: "stub-frame-revision",
			UIEffect:      &api.CommandUIEffect{Kind: "navigate", Destination: "home"},
		})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/commands/{command_id}/invoke", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusAccepted, api.CommandInvokeResponse{
			Status:        "accepted",
			FrameRevision: "stub-frame-revision",
			MessageID:     fixtureSessionID,
		})
	})
	mux.HandleFunc("GET /v1/extensions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, stubExtensionsCatalogView())
	})
	mux.HandleFunc("GET /v1/extensions/suggestions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ExtensionSuggestionsResponse{
			ProjectID:   "00000000-0000-0000-0000-000000000001",
			Suggestions: []api.ExtensionSuggestion{},
		})
	})
	mux.HandleFunc("POST /v1/extensions/suggestions/accept", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, stubExtensionsCatalogView())
	})
	mux.HandleFunc("GET /v1/extensions/units/{unit_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ExtensionUnitDetail{
			ID: "workflows/plan", Kind: "workflows", Status: api.ExtensionUnitStatusLoaded,
			Contributions: []api.ExtensionUnitContribution{},
		})
	})
	mux.HandleFunc("POST /v1/extensions/packs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.ExtensionInstallResponse{
			View: stubExtensionsCatalogView(), PackID: "acme/test", PackageRoot: "/tmp",
		})
	})
	mux.HandleFunc("PATCH /v1/extensions/configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, stubExtensionMutationResponse())
	})
	mux.HandleFunc("PATCH /v1/extensions/packs/{pack_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, stubExtensionMutationResponse())
	})
	mux.HandleFunc("DELETE /v1/extensions/packs/{pack_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PATCH /v1/extensions/units/{unit_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, stubExtensionMutationResponse())
	})
	mux.HandleFunc("POST /v1/extensions/packs/{pack_id}/profiles/{profile_name}/apply", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, stubExtensionMutationResponse())
	})
	mux.HandleFunc("GET /v1/extensions/packs/{pack_id}/update", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ExtensionPackUpdateStatus{
			PackID: "acme/test", Available: false, Changes: []api.ExtensionPackageChange{},
		})
	})
	mux.HandleFunc("POST /v1/extensions/packs/{pack_id}/update", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ExtensionPackActionResponse{
			View: stubExtensionsCatalogView(), PackID: "acme/test", ResolvedRevision: "abc123",
		})
	})
	mux.HandleFunc("POST /v1/extensions/packs/{pack_id}/reload", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ExtensionPackActionResponse{View: stubExtensionsCatalogView(), PackID: "acme/test"})
	})
	mux.HandleFunc("POST /v1/extensions/meta-packs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, stubExtensionMutationResponse())
	})
	mux.HandleFunc("PATCH /v1/extensions/meta-packs/{meta_pack_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, stubExtensionMutationResponse())
	})
	mux.HandleFunc("DELETE /v1/extensions/meta-packs/{meta_pack_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/extensions/meta-packs/{meta_pack_id}/apply", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, stubExtensionMutationResponse())
	})
}
