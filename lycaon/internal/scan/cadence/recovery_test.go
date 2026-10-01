package cadence

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCadenceSettlesScanThatFinishesBeforeSeriesBinding(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	cadence.Coordinator = &immediateTerminalCoordinator{
		ScanCoordinator: cadence.Coordinator,
		store:           store,
	}
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)

	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	got, err := cadence.RequestFull(context.Background(), dir, nil, api.ScanTriggerManual, scanbase.FullScanContext{})
	testutil.FailErr(t, "RequestFull", err)
	if len(got.ScanIDs()) != 3 || !got.Finished() {
		t.Fatalf("full pass = %+v want 3 finished scans", got)
	}
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	series, err := store.ListSeriesForPath(context.Background(), canonical)
	testutil.FailErr(t, "ListSeriesForPath", err)
	for _, row := range series {
		if row.ActiveScanID != "" || row.LastSuccessfulScanID == "" {
			t.Fatalf("terminal series not settled: %#v", row)
		}
	}
}

func TestCadenceSettleFailureRearmsAndRetries(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	cadence.Coordinator = &failingCoordinator{ScanCoordinator: cadence.Coordinator, failures: 3}
	dir := t.TempDir()
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	testutil.FailErr(t, "NoteWrites", noteCadenceWrites(context.Background(), cadence, dir, []string{"main.go"}))

	clock.Advance(50 * time.Second)
	cadence.Tick(context.Background())
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	scans, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "ListByCanonicalPath after failure", err)
	if len(scans) != 0 {
		t.Fatalf("failed pack enqueued %d scans", len(scans))
	}
	series, err := store.ListSeriesForPath(context.Background(), canonical)
	testutil.FailErr(t, "ListSeriesForPath", err)
	for _, row := range series {
		if row.DueAt.IsZero() || !row.DueAt.After(clock.Now()) {
			t.Fatalf("scanner %q not re-armed: due=%v now=%v", row.ScannerID, row.DueAt, clock.Now())
		}
	}

	clock.Advance(46 * time.Second)
	cadence.Tick(context.Background())
	scans, err = store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "ListByCanonicalPath after retry", err)
	if len(scans) != 3 {
		t.Fatalf("retry pack len = %d want 3", len(scans))
	}
}

func TestCadenceRecoversAbandonedDispatchClaims(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	seedNonEmptyProject(t, dir)

	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	rows, err := store.ListSeriesForPath(context.Background(), canonical)
	testutil.FailErr(t, "ListSeriesForPath", err)
	scannerIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		scannerIDs = append(scannerIDs, row.ScannerID)
	}
	const passID = "abandoned-pass"
	testutil.FailErr(t, "record abandoned pass", store.InsertFullPass(context.Background(), scanbase.FullPassDraft{
		ID: passID, CanonicalPath: canonical, Scanners: scannerIDs, Trigger: api.ScanTriggerManual, RequestedAt: clock.Now(),
	}))
	for i := range rows {
		rows[i].DispatchToken = "abandoned-" + rows[i].ScannerID
		rows[i].DispatchPassID = passID
		rows[i].DispatchTrigger = api.ScanTriggerManual
		rows[i].UpdatedAt = clock.Now().Add(-scanbase.DispatchClaimTTL - time.Second)
		testutil.FailErr(t, "Upsert abandoned dispatch", store.UpsertSeries(context.Background(), rows[i]))
	}

	cadence.Tick(context.Background())
	scans, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "ListByCanonicalPath", err)
	if len(scans) != 3 {
		t.Fatalf("recovered dispatch pack len = %d want 3", len(scans))
	}
	for _, scan := range scans {
		if scan.Trigger != api.ScanTriggerManual || scan.TargetKind != api.ScanTargetFull {
			t.Fatalf("recovered scan = %+v, want the abandoned full pass", scan)
		}
	}
}
