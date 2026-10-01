package cadence

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type fixedHeadSHA struct{ sha string }

func (f fixedHeadSHA) HeadSHA(context.Context, string) (string, error) { return f.sha, nil }

func seedNonEmptyProject(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
}

func defaultTestRegistry() *scanbase.MockRegistry {
	return &scanbase.MockRegistry{Scanners: []scanbase.CodeScanner{
		&scanbase.MockScanner{IDVal: "lycaon-sca", CategoryList: []api.ScanCategory{api.ScanCategorySCA, api.ScanCategorySecurity}},
		&scanbase.MockScanner{IDVal: "lycaon-secrets", CategoryList: []api.ScanCategory{api.ScanCategorySecret, api.ScanCategorySecurity}},
		&scanbase.MockScanner{IDVal: "lycaon-sast", CategoryList: []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity}},
	}}
}

func newAttachCadence(t *testing.T, store *scanbase.SQLStore, coord scanbase.ScanCoordinator, registry scanbase.CodeScannerRegistry) *Service {
	t.Helper()
	secStore, err := settings.NewSecurityScannersStoreAt(filepath.Join(t.TempDir(), "missing.yaml"))
	testutil.FailErr(t, "security scanners store", err)
	return New(store, coord, registry, secStore, scancfg.DefaultGatesConfig(), nil)
}

// requireSeriesBased asserts every selected scanner holds the root's current
// generation as its base and returns the rows.
func requireSeriesBased(t *testing.T, cadence *Service, projectDir string) []scanbase.SeriesRow {
	t.Helper()
	canonical, err := scanbase.CanonicalPath(projectDir)
	testutil.FailErr(t, "CanonicalPath", err)
	series, err := cadence.Store.ListSeriesForPath(t.Context(), canonical)
	testutil.FailErr(t, "ListSeriesForPath", err)
	if len(series) == 0 {
		t.Fatal("scan series missing")
	}
	for _, row := range series {
		if row.LastCoveredSnapshotID == "" || row.LastCoveredExecutionFingerprint == "" {
			t.Fatalf("scanner %q has no base after attach: %+v", row.ScannerID, row)
		}
		if row.DispatchToken != "" || !row.DueAt.IsZero() {
			t.Fatalf("scanner %q still armed after attach: %+v", row.ScannerID, row)
		}
	}
	return series
}

func TestAttachRecordsEveryScannersBaseWithoutScanning(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "baseline.db")
	store := scanbase.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, fixedHeadSHA{sha: "deadbeef"})
	registry := defaultTestRegistry()
	projectDir := t.TempDir()
	seedNonEmptyProject(t, projectDir)

	cadence := newAttachCadence(t, store, coord, registry)
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), projectDir))
	canonical, err := scanbase.CanonicalPath(projectDir)
	testutil.FailErr(t, "CanonicalPath", err)
	scans, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "ListByCanonicalPath", err)
	if len(scans) != 0 {
		t.Fatalf("attach scanned %d rows; automatic scanning is incremental only", len(scans))
	}
	series := requireSeriesBased(t, cadence, projectDir)
	if len(series) != len(registry.List()) {
		t.Fatalf("series = %d, want one per selected scanner", len(series))
	}
	snapshot, _, err := coord.PublishSourceGeneration(context.Background(), canonical)
	testutil.FailErr(t, "publish generation", err)
	for _, row := range series {
		if row.LastCoveredSnapshotID != snapshot.ID {
			t.Fatalf("scanner %q base = %q, want the attach generation %q", row.ScannerID, row.LastCoveredSnapshotID, snapshot.ID)
		}
	}
}

func TestAttachOnEmptyRootStillRecordsABase(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "baseline-empty.db")
	store := scanbase.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	projectDir := t.TempDir()

	cadence := newAttachCadence(t, store, coord, defaultTestRegistry())
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), projectDir))
	requireSeriesBased(t, cadence, projectDir)
}

func TestRequestFullScansEveryAdmittedFileOnce(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "baseline-full.db")
	store := scanbase.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	registry := defaultTestRegistry()
	projectDir := t.TempDir()
	seedNonEmptyProject(t, projectDir)
	cadence := newAttachCadence(t, store, coord, registry)
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), projectDir))

	first, err := cadence.RequestFull(context.Background(), projectDir, nil, api.ScanTriggerManual, scanbase.FullScanContext{})
	testutil.FailErr(t, "RequestFull", err)
	if first.ID == "" || len(first.Members) != len(registry.List()) || !first.Started() {
		t.Fatalf("full pass = %+v, want one started scan per scanner", first)
	}
	for _, member := range first.Members {
		scan := member.Scan
		if scan == nil || scan.TargetKind != api.ScanTargetFull || scan.Trigger != api.ScanTriggerManual || scan.AssessmentID != first.ID {
			t.Fatalf("full pass member = %+v", member)
		}
	}

	// A second request while the pass runs joins it: no new rows.
	second, err := cadence.RequestFull(context.Background(), projectDir, []string{"lycaon-sast"}, api.ScanTriggerScanPack, scanbase.FullScanContext{})
	testutil.FailErr(t, "RequestFull again", err)
	if second.ID != first.ID {
		t.Fatalf("second request did not join the running pass: %+v", second)
	}
	canonical, err := scanbase.CanonicalPath(projectDir)
	testutil.FailErr(t, "CanonicalPath", err)
	scans, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "ListByCanonicalPath", err)
	if len(scans) != len(registry.List()) {
		t.Fatalf("scan rows = %d, want the one running pass", len(scans))
	}
}

func TestRequestFullRejectsAScannerThatIsNotSelected(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "baseline-unknown.db")
	store := scanbase.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	projectDir := t.TempDir()
	seedNonEmptyProject(t, projectDir)
	cadence := newAttachCadence(t, store, coord, defaultTestRegistry())
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), projectDir))

	_, err := cadence.RequestFull(context.Background(), projectDir, []string{"not-a-scanner"}, api.ScanTriggerManual, scanbase.FullScanContext{})
	var selection *scancatalog.ErrInvalidScanEngineSelection
	if !errors.As(err, &selection) {
		t.Fatalf("unknown scanner error = %v", err)
	}
	if _, err := cadence.RequestFull(context.Background(), projectDir, nil, api.ScanTriggerWriteBurst, scanbase.FullScanContext{}); err == nil {
		t.Fatal("an automatic trigger must not start a full pass")
	}
}
