package hostcontracts

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/scan"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestExportCodeScanSARIFRejectsEveryNonCompleteStatus(t *testing.T) {
	statuses := []wire.CodeScanStatus{
		wire.CodeScanStatusPending,
		wire.CodeScanStatusRunning,
		wire.CodeScanStatusFailed,
		wire.CodeScanStatusTimedOut,
		wire.CodeScanStatusCanceled,
		wire.CodeScanStatusSuperseded,
	}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			coordinator := &contractfixture.FixedScanCoordinator{Scan: wire.CodeScan{
				ID: "scan-id", Categories: []wire.ScanCategory{wire.ScanCategorySAST},
				Status: status, CreatedAt: time.Now().UTC(),
			}}
			srv := contractfixture.NewTestServer(t, func(d *hostapi.Dependencies) {
				d.Scans.ScanCoordinator = coordinator
				d.Scans.ScannerRegistry = &scan.MockRegistry{Scanner: &scan.MockScanner{}}
			})
			p := contractfixture.CreateProjectForTest(t, srv, t.TempDir())
			coordinator.Scan.CanonicalPath = p.Roots[0].Path

			req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/scans/scan-id/sarif", nil)
			response := httptest.NewRecorder()
			srv.ServeHTTP(response, req)
			if response.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body=%s", response.Code, response.Body.String())
			}
		})
	}
}
