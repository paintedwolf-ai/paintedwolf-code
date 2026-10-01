package contract

import (
	"net/http"
	"strings"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func registerStubScanRoutes(mux *http.ServeMux, now time.Time, writeJSON stubJSONWriter) {
	registerStubFindingLedgerRoutes(mux, writeJSON)
	mux.HandleFunc("GET /v1/projects/{id}/security", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SecurityOverview{
			ProjectID: fixtureProjectID, Enabled: true, Scanners: []api.SecurityScannerState{},
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/scans", func(w http.ResponseWriter, r *http.Request) {
		pass := api.SecurityFullPass{
			AssessmentID: fixtureScanID, RequestedAt: now, StartedAt: &now, Trigger: api.ScanTriggerManual,
			Members: []api.SecurityFullPassMember{{
				ScannerID: "lycaon-sast", Phase: api.FullPassMemberStarted,
				Scan: &api.CodeScan{ID: fixtureScanID, ScannerID: "lycaon-sast", Categories: []api.ScanCategory{api.ScanCategorySecurity}, Status: api.CodeScanStatusPending, CreatedAt: now},
			}},
		}
		writeJSON(w, http.StatusAccepted, api.FullScanResponse{
			Passes: []api.SecurityFullPass{pass},
			Overview: api.SecurityOverview{
				ProjectID: fixtureProjectID, Enabled: true, Running: &pass, Scanners: []api.SecurityScannerState{},
			},
		})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/navigation", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.MessageNavigationResponse{
			ContentSHA256: strings.Repeat("a", 64), References: []api.NavigationReference{},
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/scans", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.CodeScanPage{Scans: []api.CodeScan{{
			ID:            fixtureScanID,
			Categories:    []api.ScanCategory{api.ScanCategorySecurity},
			Status:        api.CodeScanStatusComplete,
			FindingsCount: 0,
			CreatedAt:     now,
		}}})
	})
	mux.HandleFunc("GET /v1/projects/{id}/scans/{scan_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.CodeScan{
			ID:                    fixtureScanID,
			Categories:            []api.ScanCategory{api.ScanCategorySecurity, api.ScanCategorySAST},
			Status:                api.CodeScanStatusComplete,
			FindingsCount:         1,
			SupportedQueryFilters: api.DefaultSupportedQueryFilters(),
			Findings:              []api.SecurityFinding{fixtureSecurityFinding()},
			CreatedAt:             now,
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/scans/{scan_id}/query", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ScanQueryResponse{
			ScanID:     fixtureScanID,
			Guidance:   []api.ScanGuidanceSummary{},
			TotalMatch: 0,
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/scans/{scan_id}/sarif", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/sarif+json")
		w.Header().Set("Content-Disposition", `attachment; filename="scan-`+fixtureScanID+`.sarif.json"`)
		_, _ = w.Write([]byte(`{"version":"2.1.0","$schema":"https://json.schemastore.org/sarif-2.1.0.json","runs":[]}`))
	})
}

func registerStubScannerRoutes(mux *http.ServeMux, writeJSON stubJSONWriter) {
	mux.HandleFunc("GET /v1/scanners", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ScannerListResponse{
			Rejected: map[string]api.ScannerRejectedRow{},
			Scanners: []api.ScannerSummary{{
				ID:            "lycaon-sast",
				Driver:        "bundled",
				Engine:        "opengrep",
				ScopeKind:     "source_host_floor",
				Categories:    []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
				Enabled:       true,
				CheckOK:       true,
				CatalogSource: "host",
				Runtime:       stubScannerRuntime(),
			}},
		})
	})
	mux.HandleFunc("POST /v1/scanners", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.ScannerSummary{
			ID:            "user-ext",
			Driver:        "external",
			Engine:        "user-ext",
			ScopeKind:     "source_driver",
			Categories:    []api.ScanCategory{api.ScanCategorySAST},
			Enabled:       false,
			CheckOK:       false,
			CatalogSource: "user",
			Runtime:       stubScannerRuntime(),
		})
	})
	mux.HandleFunc("GET /v1/scanners/catalog", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ScannerCatalogResponse{
			Scanners: []api.ScannerCatalogEntry{{
				ID:             "trivy_sca",
				Label:          "Trivy — dependencies",
				Binary:         "trivy",
				Categories:     []api.ScanCategory{api.ScanCategorySCA, api.ScanCategorySecurity},
				Install:        "brew install trivy",
				DocsURL:        "https://trivy.dev/docs/",
				CommandSummary: "trivy fs --format sarif",
				Added:          false,
				BinaryFound:    false,
			}},
		})
	})
	mux.HandleFunc("POST /v1/scanners/check", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ScannerCheckResponse{
			Rows: []api.ScannerCheckRow{{
				ScannerID:   "trivy_sca",
				OK:          true,
				Detail:      "Version: 0.69.3",
				BinaryFound: true,
			}},
		})
	})
	mux.HandleFunc("PUT /v1/scanners/slots/{category}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ScannerListResponse{
			Rejected: map[string]api.ScannerRejectedRow{},
			Scanners: []api.ScannerSummary{{
				ID:            "lycaon-sast",
				Driver:        "bundled",
				Engine:        "opengrep",
				ScopeKind:     "source_host_floor",
				Categories:    []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
				Enabled:       true,
				CheckOK:       true,
				CatalogSource: "host",
				Runtime:       stubScannerRuntime(),
			}},
		})
	})
	mux.HandleFunc("PATCH /v1/scanners/{scanner_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ScannerSummary{
			ID:            "lycaon-sast",
			Driver:        "bundled",
			Engine:        "opengrep",
			ScopeKind:     "source_host_floor",
			Categories:    []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
			Enabled:       false,
			CheckOK:       false,
			CatalogSource: "user",
			Runtime:       stubScannerRuntime(),
		})
	})
	mux.HandleFunc("DELETE /v1/scanners/{scanner_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/projects/{id}/scanners", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ScannerListResponse{
			Rejected: map[string]api.ScannerRejectedRow{},
			Scanners: []api.ScannerSummary{{
				ID:            "lycaon-sast",
				Driver:        "bundled",
				Engine:        "opengrep",
				ScopeKind:     "source_host_floor",
				Categories:    []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
				Enabled:       true,
				CheckOK:       true,
				CatalogSource: "project",
				Runtime:       stubScannerRuntime(),
			}},
		})
	})
	mux.HandleFunc("PATCH /v1/projects/{id}/scanners/{scanner_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ScannerSummary{
			ID:            "lycaon-sast",
			Driver:        "bundled",
			Engine:        "opengrep",
			ScopeKind:     "source_host_floor",
			Categories:    []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
			Enabled:       false,
			CheckOK:       false,
			CatalogSource: "project",
			Runtime:       stubScannerRuntime(),
		})
	})
}
