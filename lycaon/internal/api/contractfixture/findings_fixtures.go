package contractfixture

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/pkg/api"
)

func LedgerRequest(t *testing.T, srv *hostapi.Server, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := NewAuthedRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func LedgerTestProject(t *testing.T) (*hostapi.Server, api.Project) {
	t.Helper()
	dir := t.TempDir()
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600))

	store := scan.NewSQLStore(testdbfixture.Open(t, "ledger-routes.db"))
	coord := scantest.Coordinator(t, store, nil)
	registry := MultiScannerRegistry{}
	srv, _, _ := NewSettingsTestServer(t, func(d *hostapi.Dependencies) {
		gates := scancfg.DefaultGatesConfig()
		cadence := scancadence.New(store, coord, registry, d.Core.Settings.SecurityScanners, gates, &scan.TriggerService{
			Coordinator: coord, Registry: registry, Gates: gates, Settings: d.Core.Settings.SecurityScanners,
		})
		surfaceGate := &settings.ProjectSurfaceGate{
			Surface: projectcontrib.SurfaceScanConfig, Surfaces: d.Core.Settings.TrustSurfaces, Projects: d.Core.Projects,
		}
		cadence.OverlayRootsApply = surfaceGate.FilterPaths
		d.Scans.ScanCoordinator = coord
		d.Scans.ScannerRegistry = registry
		d.Scans.ScanCadence = cadence
	})

	var proj api.Project
	DecodeCreateProjectForTest(t, srv, dir, &proj)
	DrainBackground(t, srv)
	return srv, proj
}
