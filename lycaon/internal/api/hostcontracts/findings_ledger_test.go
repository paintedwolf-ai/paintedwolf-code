package hostcontracts

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestProjectFindingsQueryAnswersEmptyLedger(t *testing.T) {
	srv, proj := contractfixture.LedgerTestProject(t)
	w := contractfixture.LedgerRequest(t, srv, http.MethodPost, "/v1/projects/"+proj.ID+"/findings/query", `{"limit":25}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var page wire.FindingLedgerResponse
	testutil.FailErr(t, "decode ledger", json.Unmarshal(w.Body.Bytes(), &page))
	if page.ProjectID != proj.ID {
		t.Fatalf("project_id = %q, want %q", page.ProjectID, proj.ID)
	}
	if page.Entries == nil {
		t.Fatal("entries must be an empty array, never null")
	}
	if len(page.Entries) != 0 || page.TotalMatch != 0 {
		t.Fatalf("entries = %d, total = %d, want an empty ledger", len(page.Entries), page.TotalMatch)
	}
	if page.Counts.Open != 0 || page.Counts.Fixed != 0 {
		t.Fatalf("counts = %+v, want zeroes", page.Counts)
	}
	// by_level includes every level, zeros too.
	for _, level := range wire.AllFindingLevelValues() {
		if _, ok := page.ByLevel[string(level)]; !ok {
			t.Fatalf("by_level is missing %q", level)
		}
	}
}

func TestProjectFindingsQueryAcceptsNoBody(t *testing.T) {
	srv, proj := contractfixture.LedgerTestProject(t)
	w := contractfixture.LedgerRequest(t, srv, http.MethodPost, "/v1/projects/"+proj.ID+"/findings/query", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestFindingIgnoreRejectsAnEntryNamingNothing(t *testing.T) {
	srv, proj := contractfixture.LedgerTestProject(t)
	w := contractfixture.LedgerRequest(t, srv, http.MethodPost,
		"/v1/projects/"+proj.ID+"/findings/ignores", `{"reason":"too noisy"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var response wire.ErrorResponse
	testutil.FailErr(t, "decode response", json.Unmarshal(w.Body.Bytes(), &response))
	if response.Code != "invalid_request" {
		t.Fatalf("code = %q, want the code whose notice carries the reason", response.Code)
	}
	if reason, _ := response.Details["reason"].(string); reason == "" {
		t.Fatalf("details = %v, want the reason the entry was refused", response.Details)
	}
}

func TestFindingIgnoreRejectsAJustificationWithNoAdvisory(t *testing.T) {
	srv, proj := contractfixture.LedgerTestProject(t)
	w := contractfixture.LedgerRequest(t, srv, http.MethodPost, "/v1/projects/"+proj.ID+"/findings/ignores",
		`{"path":"test/**","reason":"fixtures","justification":"vulnerable_code_not_present"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestFindingIgnoreReportsAnUnknownEntry(t *testing.T) {
	srv, proj := contractfixture.LedgerTestProject(t)
	w := contractfixture.LedgerRequest(t, srv, http.MethodDelete,
		"/v1/projects/"+proj.ID+"/findings/ignores/not-an-entry", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var response wire.ErrorResponse
	testutil.FailErr(t, "decode response", json.Unmarshal(w.Body.Bytes(), &response))
	if response.Code != wire.ApiErrorCodeIgnoreEntryNotFound {
		t.Fatalf("code = %q, want ignore_entry_not_found", response.Code)
	}
}

func TestFindingIgnoreWritesTheProjectOverlayFile(t *testing.T) {
	srv, proj := contractfixture.LedgerTestProject(t)
	w := contractfixture.LedgerRequest(t, srv, http.MethodPost, "/v1/projects/"+proj.ID+"/findings/ignores",
		`{"path":"test/**","kind":"sast","reason":"fixture material"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var list wire.FindingIgnoreListResponse
	testutil.FailErr(t, "decode list", json.Unmarshal(w.Body.Bytes(), &list))
	body, err := os.ReadFile(list.Path)
	testutil.FailErr(t, "read overlay", err)
	for _, want := range []string{"path: test/**", "kind: sast", "reason: fixture material"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("overlay file %s does not carry %q:\n%s", list.Path, want, body)
		}
	}
}

func TestFindingIgnoreHonorsDisabledScanSurface(t *testing.T) {
	srv, proj := contractfixture.LedgerTestProject(t)
	testutil.FailErr(t, "disable scan surface", srv.Admin.Project.Trust.Settings.TrustSurfaces.PutEnabled(map[string]bool{
		projectcontrib.SurfaceScanConfig: false,
	}))
	w := contractfixture.LedgerRequest(t, srv, http.MethodPost, "/v1/projects/"+proj.ID+"/findings/ignores",
		`{"path":"test/**","reason":"fixture material"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var response wire.ErrorResponse
	testutil.FailErr(t, "decode disabled surface response", json.Unmarshal(w.Body.Bytes(), &response))
	if response.Code != "trust_surface_off" {
		t.Fatalf("code = %q, want trust_surface_off", response.Code)
	}
}

func TestFindingExportRefusesAnUnknownFormat(t *testing.T) {
	srv, proj := contractfixture.LedgerTestProject(t)
	w := contractfixture.LedgerRequest(t, srv, http.MethodPost,
		"/v1/projects/"+proj.ID+"/findings/export", `{"format":"pdf"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestFindingExportWritesSARIFAndOpenVEX(t *testing.T) {
	srv, proj := contractfixture.LedgerTestProject(t)
	for _, format := range []string{"sarif", "openvex"} {
		w := contractfixture.LedgerRequest(t, srv, http.MethodPost,
			"/v1/projects/"+proj.ID+"/findings/export", `{"format":"`+format+`"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", format, w.Code, w.Body.String())
		}
		if disposition := w.Header().Get("Content-Disposition"); !strings.Contains(disposition, "attachment") {
			t.Fatalf("%s Content-Disposition = %q", format, disposition)
		}
		var document map[string]any
		testutil.FailErr(t, "decode "+format, json.Unmarshal(w.Body.Bytes(), &document))
	}
}
