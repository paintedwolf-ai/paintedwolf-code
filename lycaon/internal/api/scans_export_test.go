package api

import (
	"context"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/scan"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type fixedScanCoordinator struct {
	errScanCoordinator
	scan wire.CodeScan
}

func (f *fixedScanCoordinator) Get(context.Context, string) (*wire.CodeScan, error) {
	out := f.scan
	return &out, nil
}

func (f *fixedScanCoordinator) Summary(context.Context, string) (*wire.CodeScan, error) {
	return scan.ApplyScanView(&f.scan, "summary"), nil
}

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
			coordinator := &fixedScanCoordinator{scan: wire.CodeScan{
				ID: "scan-id", Categories: []wire.ScanCategory{wire.ScanCategorySAST},
				Status: status, CreatedAt: time.Now().UTC(),
			}}
			srv := newTestServer(t, func(d *Dependencies) {
				d.ScanCoordinator = coordinator
				d.ScannerRegistry = &scan.MockRegistry{Scanner: &scan.MockScanner{}}
			})
			p := createProjectForTest(t, srv, t.TempDir())
			coordinator.scan.CanonicalPath = p.Roots[0].Path

			req := newAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/scans/scan-id/sarif", nil)
			response := httptest.NewRecorder()
			srv.ServeHTTP(response, req)
			if response.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func (c *fixedScanCoordinator) SnapshotStore() *sourcesnapshot.Store { return nil }
