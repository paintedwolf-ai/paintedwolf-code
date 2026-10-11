package hostcontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSecurityScannersSettingsRoundTrip(t *testing.T) {
	srv, _, _ := contractfixture.NewSettingsTestServer(t)
	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/settings/security-scanners", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", w.Code, w.Body.String())
	}
	var got wire.SecurityScannersSettingsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if got.LandedChangeScope != wire.LandedChangeScopePathScoped {
		t.Fatalf("default landed_change_scope = %q", got.LandedChangeScope)
	}
	if got.SourceVerify != wire.SourceVerifyStat {
		t.Fatalf("default source_verify = %q", got.SourceVerify)
	}

	putBody := `{"enabled":false,"landed_change_scope":"path_scoped","source_verify":"stat"}`
	putReq := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/settings/security-scanners", strings.NewReader(putBody))
	putReq.Header.Set("Content-Type", "application/json")
	putW := httptest.NewRecorder()
	srv.ServeHTTP(putW, putReq)
	if putW.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", putW.Code, putW.Body.String())
	}
	var updated wire.SecurityScannersSettingsResponse
	if err := json.Unmarshal(putW.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode PUT: %v", err)
	}
	if updated.Enabled {
		t.Fatalf("PUT not applied: %+v", updated)
	}
	if updated.LandedChangeScope != wire.LandedChangeScopePathScoped {
		t.Fatalf("landed_change_scope = %q", updated.LandedChangeScope)
	}

	fullBody := `{"enabled":true,"landed_change_scope":"full_root","source_verify":"content"}`
	fullReq := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/settings/security-scanners", strings.NewReader(fullBody))
	fullReq.Header.Set("Content-Type", "application/json")
	fullW := httptest.NewRecorder()
	srv.ServeHTTP(fullW, fullReq)
	if fullW.Code != http.StatusOK {
		t.Fatalf("PUT full_root status = %d body=%s", fullW.Code, fullW.Body.String())
	}
	var fullUpdated wire.SecurityScannersSettingsResponse
	if err := json.Unmarshal(fullW.Body.Bytes(), &fullUpdated); err != nil {
		t.Fatalf("decode full_root PUT: %v", err)
	}
	if fullUpdated.LandedChangeScope != wire.LandedChangeScopeFullRoot {
		t.Fatalf("landed_change_scope after PUT = %q", fullUpdated.LandedChangeScope)
	}
	getReq := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/settings/security-scanners", nil)
	getW := httptest.NewRecorder()
	srv.ServeHTTP(getW, getReq)
	var gotAgain wire.SecurityScannersSettingsResponse
	if err := json.Unmarshal(getW.Body.Bytes(), &gotAgain); err != nil {
		t.Fatalf("decode GET after PUT: %v", err)
	}
	if gotAgain.LandedChangeScope != wire.LandedChangeScopeFullRoot {
		t.Fatalf("GET landed_change_scope = %q", gotAgain.LandedChangeScope)
	}
	if gotAgain.SourceVerify != wire.SourceVerifyContent {
		t.Fatalf("GET source_verify = %q", gotAgain.SourceVerify)
	}
}

func TestSecurityScannersSettingsRejectsUnknownSourceVerify(t *testing.T) {
	srv, _, _ := contractfixture.NewSettingsTestServer(t)
	body := `{"enabled":true,"landed_change_scope":"path_scoped","source_verify":"sometimes"}`
	req := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/settings/security-scanners", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestCreateProjectBaselinesSecurityWithoutScanning(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "scan-bootstrap.db")
	store := scan.NewSQLStore(sqlDB)
	srv, _, _ := contractfixture.NewSettingsTestServer(t, func(d *hostapi.Dependencies) {
		coord := scantest.Coordinator(t, store, nil)
		registry := contractfixture.MultiScannerRegistry{}
		gates := scancfg.DefaultGatesConfig()
		d.Scans.ScanCoordinator, d.Scans.ScannerRegistry = coord, registry
		d.Scans.ScanCadence = scancadence.New(store, coord, registry, d.Core.Settings.SecurityScanners, gates, &scan.TriggerService{
			Coordinator: coord, Registry: registry, Gates: gates, Settings: d.Core.Settings.SecurityScanners,
		})
	})
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	var proj wire.Project
	contractfixture.DecodeCreateProjectForTest(t, srv, dir, &proj)
	contractfixture.DrainBackground(t, srv)
	rootPath := contractfixture.PrimaryRootPath(proj)
	scans, err := store.ListByCanonicalPath(t.Context(), rootPath)
	testutil.FailErr(t, "store.ListByCanonicalPath failed", err)
	if len(scans) != 0 {
		t.Fatalf("attach scanned %d rows; automatic scanning is incremental only", len(scans))
	}
	series, err := store.ListSeriesForPath(t.Context(), rootPath)
	testutil.FailErr(t, "store.ListSeriesForPath", err)
	if len(series) < 3 {
		t.Fatalf("series = %d, want one per selected scanner", len(series))
	}
	for _, row := range series {
		if row.LastCoveredSnapshotID == "" {
			t.Fatalf("scanner %q has no base after attach", row.ScannerID)
		}
	}
}

func TestCreateProjectBaselinesAnEmptyRoot(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "scan-bootstrap-empty.db")
	store := scan.NewSQLStore(sqlDB)
	srv, _, _ := contractfixture.NewSettingsTestServer(t, func(d *hostapi.Dependencies) {
		coord := scantest.Coordinator(t, store, nil)
		registry := contractfixture.MultiScannerRegistry{}
		gates := scancfg.DefaultGatesConfig()
		d.Scans.ScanCoordinator, d.Scans.ScannerRegistry = coord, registry
		d.Scans.ScanCadence = scancadence.New(store, coord, registry, d.Core.Settings.SecurityScanners, gates, &scan.TriggerService{
			Coordinator: coord, Registry: registry, Gates: gates, Settings: d.Core.Settings.SecurityScanners,
		})
	})
	dir := t.TempDir()

	var proj wire.Project
	contractfixture.DecodeCreateProjectForTest(t, srv, dir, &proj)
	contractfixture.DrainBackground(t, srv)
	rootPath := contractfixture.PrimaryRootPath(proj)
	scans, err := store.ListByCanonicalPath(t.Context(), rootPath)
	testutil.FailErr(t, "store.ListByCanonicalPath failed", err)
	if len(scans) != 0 {
		t.Fatalf("empty project enqueued %d scans", len(scans))
	}
	series, err := store.ListSeriesForPath(t.Context(), rootPath)
	testutil.FailErr(t, "store.ListSeriesForPath", err)
	if len(series) == 0 {
		t.Fatal("empty attach scan series missing")
	}
	for _, row := range series {
		if row.LastCoveredSnapshotID == "" {
			t.Fatalf("scanner %q has no base after an empty attach", row.ScannerID)
		}
	}
}
