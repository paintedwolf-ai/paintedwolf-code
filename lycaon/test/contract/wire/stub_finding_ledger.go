package contract

import (
	"encoding/json"
	"net/http"

	"github.com/lycaon/lycaon/pkg/api"
)

func registerStubFindingLedgerRoutes(mux *http.ServeMux, writeJSON stubJSONWriter) {
	mux.HandleFunc("POST /v1/projects/{id}/findings/query", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.FindingLedgerResponse{
			ProjectID: fixtureProjectID,
			Entries:   []api.FindingLedgerEntry{},
			ByLevel: map[string]int{
				"critical": 0, "high": 0, "medium": 0, "low": 0, "info": 0, "unknown": 0,
			},
		})
	})
	ignores := api.FindingIgnoreListResponse{
		ProjectID: fixtureProjectID, Path: fixtureProjectDir + "/.paintedwolf/ignores.yaml", Rules: []api.FindingIgnoreRule{},
	}
	mux.HandleFunc("GET /v1/projects/{id}/findings/ignores", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, ignores)
	})
	mux.HandleFunc("POST /v1/projects/{id}/findings/ignores", func(w http.ResponseWriter, r *http.Request) {
		result := ignores
		result.Rules = []api.FindingIgnoreRule{{
			ID: "ignore-fixture", Rule: "fixture-rule", Reason: "Fixture decision", Source: "project", Withdrawable: true,
		}}
		writeJSON(w, http.StatusCreated, result)
	})
	mux.HandleFunc("DELETE /v1/projects/{id}/findings/ignores/{entry_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/projects/{id}/findings/export", func(w http.ResponseWriter, r *http.Request) {
		var req api.FindingExportRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Code: api.ApiErrorCodeInvalidRequest, Message: "Invalid export request"})
			return
		}
		var contentType, filename, body string
		switch req.Format {
		case api.FindingExportSARIF:
			contentType, filename = "application/sarif+json", "findings.sarif"
			body = `{"version":"2.1.0","$schema":"https://json.schemastore.org/sarif-2.1.0.json","runs":[]}`
		case api.FindingExportOpenVEX:
			contentType, filename = "application/json", "findings.openvex.json"
			body = `{"@context":"https://openvex.dev/ns/v0.2.0","@id":"urn:uuid:22222222-2222-4222-8222-222222222222","author":"Fixture","timestamp":"2026-01-01T00:00:00Z","version":1,"statements":[]}`
		default:
			writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Code: api.ApiErrorCodeInvalidRequest, Message: "Unknown export format"})
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		_, _ = w.Write([]byte(body))
	})
}
