package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestScanQueryUnavailableResultsPreserveStatusAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		status  string
		message string
		action  string
	}{
		{"pending", "queued", "Wait for the scan"},
		{"running", "still running", "Wait for the scan"},
		{"failed", "failed", "Select a completed run"},
		{"timed_out", "time limit", "Select a completed run"},
		{"canceled", "canceled", "Select a completed run"},
		{"superseded", "newer run", "Select a completed run"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			coordinator := &errScanCoordinator{getErr: &scan.DrilldownReject{
				Code: scan.DrilldownRejectNotComplete,
				Data: map[string]any{"scan_id": "scan-1", "status": tc.status},
			}}
			srv := newTestServer(t, func(d *Dependencies) { d.ScanCoordinator = coordinator })
			p := createProjectForTest(t, srv, t.TempDir())
			coordinator.summary = &wire.CodeScan{ID: "scan-1", CanonicalPath: p.Roots[0].Path}
			req := newAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/scans/scan-1/query", strings.NewReader(`{"fixed_since_at":"2026-09-10T12:48:00Z"}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
			}
			var response wire.ErrorResponse
			testutil.FailErr(t, "decode response", json.Unmarshal(w.Body.Bytes(), &response))
			if response.Code != "scan_not_complete" || response.Details["status"] != tc.status || response.Scope != "project" {
				t.Fatalf("scan state was lost: %+v", response)
			}
			if !strings.Contains(response.Message, tc.message) || !strings.Contains(response.SuggestedAction, tc.action) {
				t.Fatalf("scan recovery = %+v", response)
			}
		})
	}
}
