package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestAPIScanSARIFExportHappyPath(t *testing.T) {
	projectDir := scanFixtureDir(t)
	h := wiring.BuildForTest(t, wiring.WithBundledScanners())
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)
	created := enqueueCodeScan(t, h.Server, projectDir, []wire.ScanCategory{wire.ScanCategorySecret})
	got := waitScanComplete(t, h.Server, created.ProjectID, created.ID, 90*time.Second)
	if got.Status != wire.CodeScanStatusComplete {
		t.Fatalf("scan status = %q", got.Status)
	}

	req := authedRequest(t, http.MethodGet, scanURL(created.ProjectID, created.ID)+"/sarif", nil)
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/projects/{id}/scans/{scan_id}/sarif status = %d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/sarif+json" {
		t.Fatalf("content-type = %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"version"`) || !strings.Contains(body, `"runs"`) {
		t.Fatalf("body missing SARIF envelope: %s", body)
	}
	AssertResponseMatchesOpenAPI(t, w, http.MethodGet, "/v1/projects/{id}/scans/{scan_id}/sarif", map[string]string{"id": created.ProjectID, "scan_id": created.ID})
}

func TestAPIScanSARIFExportRejectsIncomplete(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithBundledScanners())
	projectDir := t.TempDir()
	created := enqueueCodeScan(t, h.Server, projectDir, []wire.ScanCategory{wire.ScanCategorySecret})

	req := authedRequest(t, http.MethodGet, scanURL(created.ProjectID, created.ID)+"/sarif", nil)
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("inflight export status = %d body=%s", w.Code, w.Body.String())
	}
	AssertResponseMatchesOpenAPI(t, w, http.MethodGet, "/v1/projects/{id}/scans/{scan_id}/sarif", map[string]string{"id": created.ProjectID, "scan_id": created.ID})
}
