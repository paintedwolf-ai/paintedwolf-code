package cadence

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCadenceDeadlineWakesWithoutAnotherWrite(t *testing.T) {
	cadence, store := newTestCadence(t, newTestClock())
	cadence.Now = nil
	cadence.Gates.Gates.Cadence.WriteBurstSettleMs = 100
	root := t.TempDir()
	testutil.FailErr(t, "attach root", cadence.BaselineRoot(t.Context(), root))
	canonical, err := scanbase.CanonicalPath(root)
	testutil.FailErr(t, "canonical root", err)
	scantest.RunService(t, cadence.Run)
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600))
	testutil.FailErr(t, "note source", noteCadenceWrites(t.Context(), cadence, root, []string{"main.go"}))
	testutil.WaitFor(t, 10*time.Second, func() bool {
		rows, err := store.ListByCanonicalPath(t.Context(), canonical)
		return err == nil && len(rows) == 3
	})
}

func TestCadenceSleepsAfterBaseline(t *testing.T) {
	cadence, store := newTestCadence(t, newTestClock())
	cadence.Now = nil
	testutil.FailErr(t, "attach root", cadence.BaselineRoot(t.Context(), t.TempDir()))
	database := &scantest.CountedDB{Handle: store.DB()}
	cadence.Store = scanbase.NewSQLStore(database)
	scantest.RunService(t, cadence.Run)
	testutil.WaitFor(t, 5*time.Second, func() bool { return database.Reads.Load() > 0 })
	scantest.AssertQuiet(t, database)
}

func TestCadenceDeadlinesIgnoreWorkBlockedByActiveScan(t *testing.T) {
	cadence, store := newTestCadence(t, newTestClock())
	cadence.Now = nil
	root := t.TempDir()
	testutil.FailErr(t, "insert active scan", store.Insert(t.Context(), api.CodeScan{
		ID: "active", CanonicalPath: root, ScannerID: "mock", Status: api.CodeScanStatusRunning,
		Categories: []api.ScanCategory{api.ScanCategorySecurity}, SourceSnapshotID: "snapshot",
	}, nil, ""))
	testutil.FailErr(t, "record pending successor", store.UpsertSeries(t.Context(), scanbase.SeriesRow{
		CanonicalPath: root, ScannerID: "mock", DueAt: time.Now().Add(-time.Second),
		DesiredTrigger: api.ScanTriggerWriteBurst, DesiredPaths: []string{"main.go"},
	}))
	if delay := cadence.nextWake(t.Context(), time.Minute); delay != time.Minute {
		t.Fatalf("active scan kept dispatch hot: next wake %s", delay)
	}
	row, err := store.GetSeries(t.Context(), root, "mock")
	testutil.FailErr(t, "read series", err)
	row.DispatchToken = "abandoned"
	row.ClaimHeartbeatAt = time.Now().Add(-scanbase.DispatchClaimTTL + 2*time.Second)
	testutil.FailErr(t, "record dispatch lease", store.UpsertSeries(t.Context(), *row))
	if delay := cadence.nextWake(t.Context(), time.Minute); delay <= 0 || delay > 2*time.Second {
		t.Fatalf("dispatch recovery deadline = %s", delay)
	}
}
