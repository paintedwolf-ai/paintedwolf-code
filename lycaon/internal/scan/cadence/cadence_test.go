package cadence

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCadenceEmptyAttachRecordsABaseAndScansNothing(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()

	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	scans, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "ListByCanonicalPath", err)
	if len(scans) != 0 {
		t.Fatalf("empty attach enqueued %d scans", len(scans))
	}
	requireSeriesBased(t, cadence, dir)
}

func TestCadenceFirstWriteAfterAttachScansOnlyTheChange(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))

	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	testutil.FailErr(t, "NoteWrites", noteCadenceWrites(context.Background(), cadence, dir, []string{"main.go"}))

	clock.Advance(2 * time.Second)
	cadence.Tick(context.Background())
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	scans, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "ListByCanonicalPath", err)
	if len(scans) != 0 {
		t.Fatalf("burst fired before the write settled: %d scans", len(scans))
	}

	clock.Advance(3 * time.Second)
	cadence.Tick(context.Background())
	scans, err = store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "ListByCanonicalPath after settle", err)
	if len(scans) != 3 {
		t.Fatalf("delta scans = %d want one per scanner", len(scans))
	}
	for _, sc := range scans {
		if sc.Trigger != api.ScanTriggerWriteBurst || sc.TargetKind != api.ScanTargetPaths || len(sc.TargetPaths) != 1 || sc.TargetPaths[0] != "main.go" {
			t.Fatalf("delta scan = %+v, want a write_burst over main.go", sc)
		}
	}
}

func TestCadenceNonEmptyAttachScansNothing(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)

	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	scans, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "ListByCanonicalPath", err)
	if len(scans) != 0 {
		t.Fatalf("attach scanned %d rows; what the tree holds is asked for, never implied", len(scans))
	}
	requireSeriesBased(t, cadence, dir)
}

func TestCadenceRunsExplicitlySelectedExternalScanner(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)
	external := &scanbase.MockRegistry{
		Scanner: &scanbase.MockScanner{IDVal: "device-sast", CategoryList: []api.ScanCategory{api.ScanCategorySAST}},
		Driver:  scancatalog.DriverExternal,
		Command: []string{"true"},
	}
	cadence.Registry = external

	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	got, err := cadence.RequestFull(context.Background(), dir, nil, api.ScanTriggerManual, scanbase.FullScanContext{})
	testutil.FailErr(t, "RequestFull", err)
	if len(got.ScanIDs()) != 1 {
		t.Fatalf("external full pass = %#v", got)
	}
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	scans, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "ListByCanonicalPath", err)
	if len(scans) != 1 || scans[0].ScannerID != "device-sast" {
		t.Fatalf("external cadence scans = %#v", scans)
	}
}

func TestCadenceLongRunningGenerationCoalescesOneSuccessor(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)

	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	_, err := cadence.RequestFull(context.Background(), dir, nil, api.ScanTriggerManual, scanbase.FullScanContext{})
	testutil.FailErr(t, "RequestFull", err)
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	initial, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "list initial generation", err)
	if len(initial) != 3 {
		t.Fatalf("initial generation = %d scans, want 3", len(initial))
	}
	firstSnapshot := initial[0].SourceSnapshotID
	for _, scan := range initial[1:] {
		if scan.SourceSnapshotID != firstSnapshot {
			t.Fatalf("initial pack snapshots differ: %q and %q", firstSnapshot, scan.SourceSnapshotID)
		}
	}

	testutil.FailErr(t, "write first change", os.WriteFile(filepath.Join(dir, "first.go"), []byte("package first\n"), 0o644))
	testutil.FailErr(t, "note first change", noteCadenceWrites(context.Background(), cadence, dir, []string{"first.go"}))
	clock.Advance(10 * time.Second)
	testutil.FailErr(t, "write second change", os.WriteFile(filepath.Join(dir, "second.go"), []byte("package second\n"), 0o644))
	testutil.FailErr(t, "note second change", noteCadenceWrites(context.Background(), cadence, dir, []string{"second.go"}))
	cadence.Tick(context.Background())
	stillInitial, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "list while generation active", err)
	if len(stillInitial) != 3 {
		t.Fatalf("active generation accumulated queue rows: got %d want 3", len(stillInitial))
	}

	completeCadenceScans(t, cadence, store)
	for _, scan := range initial {
		completed, err := store.Get(context.Background(), scan.ID)
		testutil.FailErr(t, "read initial completion", err)
		if completed.Status != api.CodeScanStatusComplete {
			t.Fatalf("initial scan %s status = %q want complete", scan.ID, completed.Status)
		}
	}

	clock.Advance(cadence.cadenceCfg().RefreshSettle())
	cadence.Tick(context.Background())
	all, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "list successor generation", err)
	if len(all) != 6 {
		t.Fatalf("coalesced generations = %d scans, want 6", len(all))
	}
	newSnapshot := all[0].SourceSnapshotID
	if newSnapshot == firstSnapshot {
		t.Fatalf("successor reused source snapshot %q", firstSnapshot)
	}
	for _, scan := range all[:3] {
		if scan.SourceSnapshotID != newSnapshot {
			t.Fatalf("successor pack snapshots differ: %q and %q", newSnapshot, scan.SourceSnapshotID)
		}
		if scan.TargetKind != api.ScanTargetPaths {
			t.Fatalf("successor %s scanned %q, want the delta from the covered generation", scan.ScannerID, scan.TargetKind)
		}
	}
}

func TestCadenceRetiresDeselectedSeriesWithoutCancelingActiveGeneration(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)

	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	_, err := cadence.RequestFull(context.Background(), dir, nil, api.ScanTriggerManual, scanbase.FullScanContext{})
	testutil.FailErr(t, "RequestFull", err)
	registry := cadence.Registry.(*scanbase.MockRegistry)
	registry.Scanners = registry.Scanners[1:]
	testutil.FailErr(t, "NoteWrites after deselection", noteCadenceWrites(context.Background(), cadence, dir, []string{"main.go"}))

	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	active, err := store.GetSeries(context.Background(), canonical, "lycaon-sca")
	testutil.FailErr(t, "GetSeries active deselected scanner", err)
	if active == nil || active.ActiveScanID == "" {
		t.Fatalf("deselected active series was removed early: %#v", active)
	}
	completeCadenceScans(t, cadence, store)
	retired, err := store.GetSeries(context.Background(), canonical, "lycaon-sca")
	testutil.FailErr(t, "GetSeries retired scanner", err)
	if retired != nil {
		t.Fatalf("deselected terminal series remains: %#v", retired)
	}
	completed, err := store.Get(context.Background(), active.ActiveScanID)
	testutil.FailErr(t, "Get completed deselected scan", err)
	if completed == nil || completed.Status != api.CodeScanStatusComplete {
		t.Fatalf("deselected in-flight scan did not complete immutably: %#v", completed)
	}
}
