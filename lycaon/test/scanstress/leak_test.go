//go:build scanstress

package scanstress

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// openFDs counts this process's open file descriptors.
func openFDs(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("lsof", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		// lsof exits non-zero when some entries are unreadable; the count still holds.
		if len(out) == 0 {
			t.Fatalf("lsof: %v", err)
		}
	}
	lines := 0
	for _, b := range out {
		if b == '\n' {
			lines++
		}
	}
	return lines - 1
}

func repeatCount() int {
	if v := os.Getenv("SCANSTRESS_REPEATS"); v != "" {
		var n int
		if _, err := fmtSscan(v, &n); err == nil && n > 0 {
			return n
		}
	}
	return 6
}

// TestRepeatedInterruptsStayBounded cancels the same scanner repeatedly and
// checks that per-run host resources do not accumulate: goroutines, file
// descriptors, engine processes, and run directories.
func TestRepeatedInterruptsStayBounded(t *testing.T) {
	scanner := newScanner(t, "stress-repeat", scancatalog.RuntimePolicy{})
	project := writeCorpus(t, corpusSpec{Files: corpusFiles(), VulnPerFile: 3})

	// One interrupted run first, so lazily allocated resources exist before the baseline.
	warmCtx, warmCancel := context.WithCancel(t.Context())
	warmObs := startObserver(t, 200*time.Millisecond)
	go func() {
		_, _ = scanner.Run(warmCtx, scan.ScanRequest{ProjectDir: project, Categories: []api.ScanCategory{api.ScanCategorySAST}})
	}()
	awaitEngineRunning(t)
	warmCancel()
	awaitNoEngineProcesses(t)
	warmObs.finish()
	runtime.GC()

	baseGoroutines := runtime.NumGoroutine()
	baseFDs := openFDs(t)
	var allRunDirs []string

	for i := 0; i < repeatCount(); i++ {
		ctx, cancel := context.WithCancel(t.Context())
		obs := startObserver(t, 150*time.Millisecond)
		done := make(chan error, 1)
		go func() {
			_, err := scanner.Run(ctx, scan.ScanRequest{ProjectDir: project, Categories: []api.ScanCategory{api.ScanCategorySAST}})
			done <- err
		}()
		awaitEngineRunning(t)
		awaitEngineWorking(t, os.Getpid())
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("iteration %d: canceled scan returned %v, want context.Canceled", i, err)
		}
		awaitNoEngineProcesses(t)
		allRunDirs = append(allRunDirs, obs.seenRunDirs()...)
		obs.finish()
	}

	// Sampling goroutines wind down after their observers finish; a leak never does.
	var endGoroutines, endFDs int
	testutil.WaitFor(t, "goroutines and descriptors return to under one per run", func() bool {
		runtime.GC()
		endGoroutines, endFDs = runtime.NumGoroutine(), openFDs(t)
		return endGoroutines-baseGoroutines < repeatCount() && endFDs-baseFDs < repeatCount()
	})

	leaked := survivingDirs(allRunDirs)
	if len(leaked) > 0 {
		t.Errorf("run directories accumulated across %d interrupted scans: %v", repeatCount(), leaked)
	}
	// Sampling goroutines are transient; a per-iteration slope is what matters.
	goroutineSlope := float64(endGoroutines-baseGoroutines) / float64(repeatCount())
	fdSlope := float64(endFDs-baseFDs) / float64(repeatCount())
	reportf(t, "repeats=%d goroutines %d->%d (slope %.2f/run) fds %d->%d (slope %.2f/run) run_dirs_seen=%d leaked=%d",
		repeatCount(), baseGoroutines, endGoroutines, goroutineSlope, baseFDs, endFDs, fdSlope, len(allRunDirs), len(leaked))
	if goroutineSlope >= 1 {
		t.Errorf("goroutines grew by %.2f per interrupted scan (%d -> %d)", goroutineSlope, baseGoroutines, endGoroutines)
	}
	if fdSlope >= 1 {
		t.Errorf("file descriptors grew by %.2f per interrupted scan (%d -> %d)", fdSlope, baseFDs, endFDs)
	}
}
