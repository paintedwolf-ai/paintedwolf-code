package execution

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRunnerRecoversLeaseAtItsDeadline(t *testing.T) {
	store := scanbase.NewSQLStore(testdbfixture.Open(t, "scan.db"))
	expiry := time.Now().Add(200 * time.Millisecond)
	testutil.FailErr(t, "insert abandoned execution", store.Insert(t.Context(), api.CodeScan{
		ID: "abandoned", CanonicalPath: t.TempDir(), ScannerID: "mock",
		Status: api.CodeScanStatusRunning, Categories: []api.ScanCategory{api.ScanCategorySecurity},
		SourceSnapshotID: "snapshot", ClaimToken: "lost-host", LeaseExpiresAt: &expiry,
	}, nil, ""))
	runner := NewRunner(scanbase.NewSQLStore(store.DB()), &scanbase.MockRegistry{Scanner: &scanbase.MockScanner{}}, scanbase.NoopIngester{}, scancfg.DefaultRunnerConfig(), nil)
	terminal := make(chan api.CodeScan, 1)
	runner.OnTerminal = func(_ context.Context, scan api.CodeScan) { terminal <- scan }
	scantest.RunService(t, runner.Run)
	select {
	case row := <-terminal:
		if row.ID != "abandoned" || row.Status != api.CodeScanStatusFailed || row.FailureCode != "SCAN_LEASE_EXPIRED" {
			t.Fatalf("lease recovery = %+v", row)
		}
	case <-time.After(testutil.Timeout(5 * time.Second)):
		t.Fatal("lease expiry waited for the idle recovery sweep")
	}
}

func TestSettingsDisableCancelsClaimBeforeEngineAdmission(t *testing.T) {
	store := scanbase.NewSQLStore(testdbfixture.Open(t, "scan.db"))
	configuration, err := settings.NewSecurityScannersStoreAt(filepath.Join(t.TempDir(), "security.yaml"))
	testutil.FailErr(t, "create settings", err)
	probe := &cancellationProbeScanner{started: make(chan struct{}), stopped: make(chan struct{})}
	runner := NewRunner(store, &scanbase.MockRegistry{Scanner: probe}, scanbase.NoopIngester{}, scancfg.DefaultRunnerConfig(), nil)
	runner.Settings = configuration
	runner.Snapshots = testSnapshots(t, store)
	runner.Broker = backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{
		backgroundwork.ResourceCPU: {Total: 1},
	})
	release, err := runner.Broker.Acquire(t.Context(), backgroundwork.Request{
		Resources: []backgroundwork.Resource{backgroundwork.ResourceCPU},
	})
	testutil.FailErr(t, "occupy CPU admission", err)
	defer release()
	root := t.TempDir()
	testutil.FailErr(t, "insert waiting scan", store.Insert(t.Context(), api.CodeScan{
		ID: "waiting-for-admission", CanonicalPath: root, ScannerID: probe.ID(),
		Status: api.CodeScanStatusPending, Categories: probe.Categories(),
		SourceSnapshotID: publishTestSnapshot(t, runner.Snapshots, root),
	}, nil, ""))
	terminal := make(chan api.CodeScan, 1)
	runner.OnTerminal = func(_ context.Context, scan api.CodeScan) { terminal <- scan }
	scantest.RunService(t, runner.Run)
	testutil.WaitFor(t, 5*time.Second, func() bool {
		count, err := store.CountRunning(t.Context())
		return err == nil && count == 1
	})
	off := false
	testutil.FailErr(t, "disable scanners", configuration.PutGlobal(settings.SecurityScannersUserOverlay{Enabled: &off}))
	select {
	case scan := <-terminal:
		if scan.Status != api.CodeScanStatusCanceled || scan.FailureCode != "SCAN_CANCELED" {
			t.Fatalf("waiting scan was not canceled: %+v", scan)
		}
	case <-time.After(testutil.Timeout(2 * time.Second)):
		t.Fatal("claimed work waited for admission or the lease heartbeat before stopping")
	}
	select {
	case <-probe.started:
		t.Fatal("disabled scanner entered the engine")
	default:
	}
}
