//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanexecution "github.com/lycaon/lycaon/internal/scan/execution"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScanRunnerIntegrationPendingToComplete(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	mock := &scan.MockScanner{IDVal: "mock", Result: &scanoutput.Result{FindingsCount: 3}}
	reg := &scan.MockRegistry{Scanner: mock}

	cfg := scancfg.DefaultRunnerConfig()
	runner := scanexecution.NewRunner(store, reg, scan.NoopIngester{}, cfg, nil)
	runner.Snapshots = coord.SnapshotStore()

	dir := testProjectDir(t)
	created, err := coord.Enqueue(context.Background(), scan.EnqueueRequest{
		ProjectDir: dir,
		Categories: []api.ScanCategory{api.ScanCategorySecurity},
		HeadSHA:    "sha",
		Trigger:    api.ScanTriggerManual,
	})
	testutil.FailErr(t, "coord.Enqueue failed", err)
	if created.Status != api.CodeScanStatusPending {
		t.Fatalf("status = %q", created.Status)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = runner.Run(ctx) }()

	for {
		got, err := coord.Get(context.Background(), created.ID)
		testutil.FailErr(t, "coord.Get failed", err)
		if got.Status == api.CodeScanStatusComplete {
			if got.FindingsCount != 3 {
				t.Fatalf("findings = %d", got.FindingsCount)
			}
			if mock.RunCalls != 1 {
				t.Fatalf("run calls = %d", mock.RunCalls)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out at status %q", got.Status)
		case <-time.After(25 * time.Millisecond):
		}
	}
}
