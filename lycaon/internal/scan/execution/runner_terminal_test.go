package execution_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanexecution "github.com/lycaon/lycaon/internal/scan/execution"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type terminalRecorder struct {
	mu    sync.Mutex
	scans []api.CodeScan
}

func (r *terminalRecorder) record(_ context.Context, scan api.CodeScan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scans = append(r.scans, scan)
}

func (r *terminalRecorder) snapshot() []api.CodeScan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]api.CodeScan(nil), r.scans...)
}

func runTerminalRunner(t *testing.T, scanner scan.CodeScanner, runtime ...scancatalog.RuntimePolicy) []api.CodeScan {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	cfg := scancfg.DefaultRunnerConfig()
	registry := &scan.MockRegistry{Scanner: scanner}
	if len(runtime) > 0 {
		registry.Runtime = runtime[0]
	}
	runner := scanexecution.NewRunner(store, registry, scan.NoopIngester{}, cfg, nil)
	runner.Snapshots = coord.SnapshotStore()
	rec := &terminalRecorder{}
	runner.OnTerminal = rec.record

	dir := testProjectDir(t)
	created, err := coord.Enqueue(context.Background(), scan.EnqueueRequest{
		ProjectDir: dir,
		Categories: []api.ScanCategory{api.ScanCategorySecurity},
		HeadSHA:    "sha",
		Trigger:    api.ScanTriggerManual,
	})
	testutil.FailErr(t, "coord.Enqueue failed", err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = runner.Run(ctx) }()

	for {
		if scans := rec.snapshot(); len(scans) > 0 {
			if scans[0].ID != created.ID {
				t.Fatalf("OnTerminal scan id = %q want %q", scans[0].ID, created.ID)
			}
			return scans
		}
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for OnTerminal")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

type deadlineScanner struct{}

func (deadlineScanner) ID() string { return "deadline" }

func (deadlineScanner) Categories() []api.ScanCategory {
	return []api.ScanCategory{api.ScanCategorySecurity}
}

func (deadlineScanner) Run(ctx context.Context, _ scan.ScanRequest) (*scanoutput.Result, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type slowSuccessScanner struct{}

func (slowSuccessScanner) ID() string { return "slow-success" }

func (slowSuccessScanner) Categories() []api.ScanCategory {
	return []api.ScanCategory{api.ScanCategorySecurity}
}

func (slowSuccessScanner) Run(ctx context.Context, _ scan.ScanRequest) (*scanoutput.Result, error) {
	timer := time.NewTimer(2500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return &scanoutput.Result{}, nil
	}
}

func TestRunnerOnTerminalFiresOnComplete(t *testing.T) {
	scans := runTerminalRunner(t, &scan.MockScanner{IDVal: "mock", Result: &scanoutput.Result{FindingsCount: 3}})
	if scans[0].Status != api.CodeScanStatusComplete {
		t.Fatalf("status = %q want complete", scans[0].Status)
	}
	if scans[0].FindingsCount != 3 {
		t.Fatalf("findings = %d want 3", scans[0].FindingsCount)
	}
}

func TestRunnerOnTerminalFiresOnFailure(t *testing.T) {
	scans := runTerminalRunner(t, &scan.MockScanner{IDVal: "mock", Err: errors.New("engine exploded")})
	if scans[0].Status != api.CodeScanStatusFailed {
		t.Fatalf("status = %q want failed", scans[0].Status)
	}
	if scans[0].Error == "" {
		t.Fatal("expected error message on failed scan")
	}
}

func TestRunnerHardRuntimeLimitIsTerminalWithoutRetry(t *testing.T) {
	scans := runTerminalRunner(t, deadlineScanner{}, scancatalog.RuntimePolicy{
		SoftLimitSec: 1, HardLimitSec: 1, CPUUnits: 1, Parallelism: 1,
	})
	if len(scans) != 1 {
		t.Fatalf("terminal callbacks = %d want 1", len(scans))
	}
	if scans[0].Status != api.CodeScanStatusTimedOut {
		t.Fatalf("status = %q want timed_out", scans[0].Status)
	}
	if scans[0].Attempt != 1 {
		t.Fatalf("attempt = %d want 1", scans[0].Attempt)
	}
}

func TestRunnerSoftRuntimeLimitPersistsLongRunningState(t *testing.T) {
	scans := runTerminalRunner(t, slowSuccessScanner{}, scancatalog.RuntimePolicy{
		SoftLimitSec: 1, HardLimitSec: 0, CPUUnits: 1, Parallelism: 1,
	})
	got := scans[0]
	if got.Status != api.CodeScanStatusComplete {
		t.Fatalf("status = %q want complete", got.Status)
	}
	if !got.LongRunning || got.LongRunningAt == nil {
		t.Fatalf("long-running state = %#v", got)
	}
	if got.Runtime == nil || got.Runtime.SoftLimitMs != 1000 || got.Runtime.HardLimitMs != 0 {
		t.Fatalf("runtime = %#v", got.Runtime)
	}
	if got.StartedAt == nil {
		t.Fatal("started_at not persisted")
	}
}
