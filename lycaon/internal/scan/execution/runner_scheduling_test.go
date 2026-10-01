package execution

import (
	"context"
	"testing"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRunnerSleepsIdleAndWakesForCommittedScan(t *testing.T) {
	database := &scantest.CountedDB{Handle: testdbfixture.Open(t, "scan.db")}
	store := scanbase.NewSQLStore(database)
	scanner := &scanbase.MockScanner{}
	runner := NewRunner(store, &scanbase.MockRegistry{Scanner: scanner}, scanbase.NoopIngester{}, scancfg.DefaultRunnerConfig(), nil)
	runner.Snapshots = testSnapshots(t, store)
	root := t.TempDir()
	snapshot := publishTestSnapshot(t, runner.Snapshots, root)
	terminal := make(chan api.CodeScan, 1)
	runner.OnTerminal = func(_ context.Context, scan api.CodeScan) { terminal <- scan }
	scantest.RunService(t, runner.Run)
	testutil.WaitFor(t, 5*time.Second, func() bool { return database.Writes.Load() > 0 })
	scantest.AssertQuiet(t, database)
	testutil.FailErr(t, "insert scan after idle", store.Insert(t.Context(), api.CodeScan{
		ID: "wake-scan", CanonicalPath: root, ScannerID: scanner.ID(),
		Categories: []api.ScanCategory{api.ScanCategorySecurity}, Status: api.CodeScanStatusPending,
		SourceSnapshotID: snapshot,
	}, nil, ""))
	select {
	case scan := <-terminal:
		if scan.ID != "wake-scan" || scan.Status != api.CodeScanStatusComplete {
			t.Fatalf("committed demand completed as %+v", scan)
		}
	case <-time.After(testutil.Timeout(10 * time.Second)):
		t.Fatal("committed demand waited for the recovery sweep")
	}
	scantest.AssertQuiet(t, database)
}

func TestDisabledRunnerSleepsAndSettingsWakeIt(t *testing.T) {
	database := &scantest.CountedDB{Handle: testdbfixture.Open(t, "scan.db")}
	store := scanbase.NewSQLStore(database)
	runner := NewRunner(store, &scanbase.MockRegistry{Scanner: &scanbase.MockScanner{}}, scanbase.NoopIngester{}, scancfg.DefaultRunnerConfig(), nil)
	runner.Settings = disabledScannerSettings(t)
	scantest.RunService(t, runner.Run)
	testutil.WaitFor(t, 5*time.Second, func() bool { return database.Writes.Load() > 0 })
	scantest.AssertQuiet(t, database)
	before := database.Reads.Load()
	on := true
	testutil.FailErr(t, "enable scanners", runner.Settings.PutGlobal(settings.SecurityScannersUserOverlay{Enabled: &on}))
	testutil.WaitFor(t, 5*time.Second, func() bool { return database.Reads.Load() > before })
	scantest.AssertQuiet(t, database)
}
