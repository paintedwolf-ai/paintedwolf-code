package execution

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func disabledScannerSettings(t *testing.T) *settings.SecurityScannersStore {
	t.Helper()
	store, err := settings.NewSecurityScannersStoreAt(filepath.Join(t.TempDir(), "security.yaml"))
	testutil.FailErr(t, "NewSecurityScannersStoreAt", err)
	off := false
	testutil.FailErr(t, "PutGlobal", store.PutGlobal(settings.SecurityScannersUserOverlay{Enabled: &off}))
	return store
}

func TestRunnerMainOffCancelsPendingScans(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	store := scanbase.NewSQLStore(sqlDB)
	rec := api.CodeScan{
		ID: "scan-pending", CanonicalPath: t.TempDir(), Categories: []api.ScanCategory{api.ScanCategorySAST},
		Status: api.CodeScanStatusPending, CreatedAt: time.Now().UTC(), SourceSnapshotID: "snapshot-1",
	}
	testutil.FailErr(t, "Insert", store.Insert(t.Context(), rec, nil, ""))
	runner := NewRunner(store, &scanbase.MockRegistry{Scanner: &scanbase.MockScanner{}}, scanbase.NoopIngester{}, scancfg.DefaultRunnerConfig(), nil)
	runner.Settings = disabledScannerSettings(t)
	terminal := make(chan api.CodeScan, 1)
	runner.OnTerminal = func(_ context.Context, rec api.CodeScan) { terminal <- rec }

	testutil.FailErr(t, "service scan work", runner.serviceWork(t.Context()))
	select {
	case notified := <-terminal:
		if notified.ID != rec.ID || notified.Status != api.CodeScanStatusCanceled {
			t.Fatalf("terminal notification = %#v", notified)
		}
	default:
		t.Fatal("main cancellation did not notify waiters")
	}
	got, err := store.Get(t.Context(), rec.ID)
	testutil.FailErr(t, "Get", err)
	if got == nil || got.Status != api.CodeScanStatusCanceled || got.Error != "security scanners disabled" {
		t.Fatalf("pending scan after main off = %#v", got)
	}
	on := true
	testutil.FailErr(t, "PutGlobal on", runner.Settings.PutGlobal(settings.SecurityScannersUserOverlay{Enabled: &on}))
	runner.MaxRunning = 0
	testutil.FailErr(t, "service scan work", runner.serviceWork(t.Context()))
	got, err = store.Get(t.Context(), rec.ID)
	testutil.FailErr(t, "Get after re-enable", err)
	if got == nil || got.Status != api.CodeScanStatusCanceled || got.Error != "security scanners disabled" {
		t.Fatalf("terminal cancellation was mutated after main on = %#v", got)
	}
}

type cancellationProbeScanner struct {
	started       chan struct{}
	stopped       chan struct{}
	returnSuccess bool
}

func (s *cancellationProbeScanner) ID() string { return "cancel-probe" }
func (s *cancellationProbeScanner) Categories() []api.ScanCategory {
	return []api.ScanCategory{api.ScanCategorySAST}
}
func (s *cancellationProbeScanner) Run(ctx context.Context, _ scanbase.ScanRequest) (*scanoutput.Result, error) {
	close(s.started)
	<-ctx.Done()
	close(s.stopped)
	if s.returnSuccess {
		return &scanoutput.Result{FindingsCount: 1}, nil
	}
	return nil, ctx.Err()
}

func TestRunnerClaimsOnlyAvailableCapacity(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	store := scanbase.NewSQLStore(sqlDB)
	snapshots := testSnapshots(t, store)
	projectDirs := []string{t.TempDir(), t.TempDir()}
	snapshotIDs := []string{
		publishTestSnapshot(t, snapshots, projectDirs[0]),
		publishTestSnapshot(t, snapshots, projectDirs[1]),
	}
	now := time.Now().UTC()
	for i, id := range []string{"scan-1", "scan-2"} {
		testutil.FailErr(t, "insert "+id, store.Insert(t.Context(), api.CodeScan{
			ID: id, CanonicalPath: projectDirs[i], Categories: []api.ScanCategory{api.ScanCategorySAST},
			ScannerID: "cancel-probe", Status: api.CodeScanStatusPending, CreatedAt: now,
			SourceSnapshotID: snapshotIDs[i],
		}, nil, ""))
	}
	probe := &cancellationProbeScanner{started: make(chan struct{}), stopped: make(chan struct{})}
	runner := NewRunner(store, &scanbase.MockRegistry{Scanner: probe}, scanbase.NoopIngester{}, scancfg.DefaultRunnerConfig(), nil)
	runner.MaxRunning = 1
	runner.Snapshots = snapshots
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	testutil.FailErr(t, "service scan work", runner.serviceWork(ctx))
	select {
	case <-probe.started:
	case <-time.After(testutil.Timeout(15 * time.Second)):
		t.Fatal("scanner did not start")
	}

	second, err := store.Get(t.Context(), "scan-2")
	testutil.FailErr(t, "load second scan", err)
	if second == nil || second.Status != api.CodeScanStatusPending {
		t.Fatalf("second scan status = %q want pending", second.Status)
	}
	cancel()
	runner.inflight.Wait()
}

func TestRunnerMainOffCancelsRunningScannerContext(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	store := scanbase.NewSQLStore(sqlDB)
	snapshots := testSnapshots(t, store)
	projectDir := t.TempDir()
	rec := api.CodeScan{
		ID: "scan-running", CanonicalPath: projectDir, Categories: []api.ScanCategory{api.ScanCategorySAST},
		ScannerID: "cancel-probe", Status: api.CodeScanStatusRunning, CreatedAt: time.Now().UTC(),
		SourceSnapshotID: publishTestSnapshot(t, snapshots, projectDir), ClaimToken: "claim-scan-running",
	}
	testutil.FailErr(t, "Insert", store.Insert(t.Context(), rec, nil, ""))
	settingsStore, err := settings.NewSecurityScannersStoreAt(filepath.Join(t.TempDir(), "security.yaml"))
	testutil.FailErr(t, "NewSecurityScannersStoreAt", err)
	probe := &cancellationProbeScanner{started: make(chan struct{}), stopped: make(chan struct{})}
	runner := NewRunner(store, &scanbase.MockRegistry{Scanner: probe}, scanbase.NoopIngester{}, scancfg.DefaultRunnerConfig(), nil)
	runner.Snapshots = snapshots
	runner.Settings = settingsStore

	done := make(chan struct{})
	go func() {
		runner.execute(t.Context(), &rec)
		close(done)
	}()
	select {
	case <-probe.started:
	case <-time.After(testutil.Timeout(15 * time.Second)):
		t.Fatal("scanner did not start")
	}
	off := false
	testutil.FailErr(t, "PutGlobal", settingsStore.PutGlobal(settings.SecurityScannersUserOverlay{Enabled: &off}))
	select {
	case <-probe.stopped:
	case <-time.After(testutil.Timeout(15 * time.Second)):
		t.Fatal("main switch did not cancel scanner context")
	}
	<-done
	got, err := store.Get(t.Context(), rec.ID)
	testutil.FailErr(t, "Get", err)
	if got == nil || got.Status != api.CodeScanStatusCanceled {
		t.Fatalf("running scan after main off = %#v", got)
	}
}

func TestRunnerInterruptedExecutionCommitsTerminalState(t *testing.T) {
	for _, tc := range []struct {
		name     string
		shutdown bool
		success  bool
	}{
		{name: "shutdown during execution", shutdown: true},
		{name: "shutdown discards late success", shutdown: true, success: true},
		{name: "hard deadline discards late success", success: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := scanbase.NewSQLStore(testdbfixture.Open(t, "store.db"))
			snapshots := testSnapshots(t, store)
			projectDir := t.TempDir()
			rec := api.CodeScan{
				ID: "interrupted-scan", CanonicalPath: projectDir, ScannerID: "cancel-probe",
				Categories: []api.ScanCategory{api.ScanCategorySAST}, Status: api.CodeScanStatusPending,
				CreatedAt: time.Now().UTC(), SourceSnapshotID: publishTestSnapshot(t, snapshots, projectDir),
			}
			testutil.FailErr(t, "insert scan", store.Insert(t.Context(), rec, nil, ""))
			probe := &cancellationProbeScanner{started: make(chan struct{}), stopped: make(chan struct{}), returnSuccess: tc.success}
			registry := &scanbase.MockRegistry{Scanner: probe}
			if !tc.shutdown {
				registry.Runtime = scancatalog.RuntimePolicy{SoftLimitSec: 1, HardLimitSec: 1, CPUUnits: 1, Parallelism: 1}
			}
			runner := NewRunner(store, registry, scanbase.NoopIngester{}, scancfg.DefaultRunnerConfig(), nil)
			runner.Snapshots = snapshots
			type notification struct {
				scan       api.CodeScan
				contextErr error
				bounded    bool
			}
			terminal := make(chan notification, 2)
			runner.OnTerminal = func(ctx context.Context, rec api.CodeScan) {
				_, bounded := ctx.Deadline()
				terminal <- notification{scan: rec, contextErr: ctx.Err(), bounded: bounded}
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			var runErr error
			go func() {
				defer close(done)
				runErr = runner.Run(ctx)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(testutil.Timeout(15 * time.Second)):
					t.Error("runner cleanup did not drain execution")
				}
			})
			select {
			case <-probe.started:
			case <-done:
				t.Fatalf("runner stopped before scanner started: %v", runErr)
			case <-time.After(testutil.Timeout(15 * time.Second)):
				t.Fatal("scanner did not start")
			}
			if tc.shutdown {
				cancel()
			}
			var notified notification
			select {
			case notified = <-terminal:
			case <-time.After(testutil.Timeout(15 * time.Second)):
				t.Fatal("interrupted scan did not notify terminal consumers")
			}
			cancel()
			select {
			case <-done:
				if !errors.Is(runErr, context.Canceled) {
					t.Fatalf("runner shutdown = %v", runErr)
				}
			case <-time.After(testutil.Timeout(15 * time.Second)):
				t.Fatal("runner did not drain its scanner on shutdown")
			}
			if notified.contextErr != nil || !notified.bounded {
				t.Fatalf("terminal context: error=%v bounded=%v", notified.contextErr, notified.bounded)
			}
			got, err := store.Get(t.Context(), rec.ID)
			testutil.FailErr(t, "load terminal scan", err)
			wantStatus, wantCode := api.CodeScanStatusTimedOut, scanbase.FailureTimeout
			if tc.shutdown {
				wantStatus, wantCode = api.CodeScanStatusCanceled, "SCAN_CANCELED"
			}
			if got == nil || got.Status != wantStatus || got.FailureCode != wantCode || got.CompletedAt == nil || got.CoverageStatus != api.ScanCoverageUnavailable || got.FindingsCount != 0 {
				t.Fatalf("durable interrupted scan = %#v", got)
			}
			if notified.scan.Status != got.Status || notified.scan.FailureCode != got.FailureCode || notified.scan.CoverageStatus != got.CoverageStatus {
				t.Fatalf("terminal notification disagrees with committed scan: %#v", notified.scan)
			}
			select {
			case extra := <-terminal:
				t.Fatalf("duplicate terminal notification: %#v", extra.scan)
			default:
			}
		})
	}
}
