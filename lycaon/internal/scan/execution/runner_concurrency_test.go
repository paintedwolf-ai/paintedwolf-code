package execution_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanexecution "github.com/lycaon/lycaon/internal/scan/execution"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRunnerConcurrencyCap(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "scan-cap.db")
	sqlDB := testdbfixture.OpenPath(t, dbPath)

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	ctx := context.Background()
	var running int32
	slow := &scan.SlowMockScanner{Delay: 50 * time.Millisecond, Running: &running}
	reg := &scan.MockRegistry{Scanner: slow}

	cfg := scancfg.DefaultRunnerConfig()
	cfg.Runner.MaxConcurrency = 2
	runner := scanexecution.NewRunner(store, reg, scan.NoopIngester{}, cfg, nil)
	runner.Snapshots = coord.SnapshotStore()

	dir := testProjectDir(t)
	for i := 0; i < 4; i++ {
		if _, err := coord.Enqueue(ctx, scan.EnqueueRequest{
			ProjectDir: dir,
			Categories: []api.ScanCategory{api.ScanCategorySecurity},
			HeadSHA:    string(rune('a' + i)),
			Trigger:    api.ScanTriggerManual,
		}); err != nil {
			testutil.FailErr(t, "coord.Enqueue", err)
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = runner.Run(runCtx)
	}()

	testutil.WaitFor(t, 2*time.Second, func() bool {
		list, err := store.ListByCanonicalPath(ctx, dir)
		testutil.FailErr(t, "store.ListByCanonicalPath failed", err)
		doneCount := 0
		for _, item := range list {
			if item.Status == api.CodeScanStatusComplete {
				doneCount++
			}
		}
		return doneCount == 4
	})

	cancel()
	<-done
}
