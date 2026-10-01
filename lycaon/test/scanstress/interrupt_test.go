//go:build scanstress

package scanstress

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/pkg/api"
)

// awaitEngineRunning blocks until this process owns a live engine descendant,
// so an interrupt lands inside real engine work rather than during startup.
func awaitEngineRunning(t *testing.T, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if len(engineProcesses(descendants(processTable(t), os.Getpid()))) > 0 {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// TestCancellationTerminatesEngineTree cancels a scan already executing engine
// work and requires the whole owned process tree and its run directory to go.
func TestCancellationTerminatesEngineTree(t *testing.T) {
	scanner := newScanner(t, "stress-cancel", scancatalog.RuntimePolicy{})
	project := writeCorpus(t, corpusSpec{Files: corpusFiles(), VulnPerFile: 3})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	obs := startObserver(t, 200*time.Millisecond)

	errCh := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := scanner.Run(ctx, scan.ScanRequest{ProjectDir: project, Categories: []api.ScanCategory{api.ScanCategorySAST}})
		errCh <- err
	}()

	if !awaitEngineRunning(t, 3*time.Minute) {
		cancel()
		<-errCh
		obs.finish()
		t.Fatal("engine never started; nothing to cancel")
	}
	// Let the engine reach steady-state work before the interrupt.
	time.Sleep(5 * time.Second)
	engineBefore := engineProcesses(descendants(processTable(t), os.Getpid()))
	cancelAt := time.Now()
	cancel()

	var runErr error
	select {
	case runErr = <-errCh:
	case <-time.After(2 * time.Minute):
		obs.finish()
		t.Fatal("Run did not return within 2m after cancellation")
	}
	returnLatency := time.Since(cancelAt)
	samples := obs.finish()

	if runErr == nil {
		t.Error("canceled scan returned a nil error; an interrupted scan must not read as a completed one")
	}
	if !errors.Is(runErr, context.Canceled) {
		reportf(t, "cancel error does not wrap context.Canceled: %v", runErr)
	}
	drain, survivors := awaitNoEngineProcesses(t, 30*time.Second)
	if len(survivors) > 0 {
		t.Errorf("engine processes survived cancellation after %s: %+v", drain, survivors)
	}
	owned := obs.seenRunDirs()
	if leaked := survivingDirs(owned); len(leaked) > 0 {
		t.Errorf("run directories leaked on the cancel path: %v", leaked)
	}
	reportf(t, "engine_procs_at_cancel=%d ran_for=%s return_latency=%s drain=%s peak_rss_mib=%.1f owned_run_dirs=%d err=%v",
		len(engineBefore), cancelAt.Sub(start).Round(time.Second), returnLatency.Round(time.Millisecond),
		drain.Round(time.Millisecond), float64(peak(samples).AggregateRSS)/1024, len(owned), runErr)
}

// TestHardLimitTimeoutTerminatesEngineTree drives the scanner's own runtime
// policy to a hard limit shorter than this corpus needs, then requires an
// honest timeout and a fully reaped tree. The limit is derived from the
// measured baseline, not chosen to fire during startup.
func TestHardLimitTimeoutTerminatesEngineTree(t *testing.T) {
	hard := 45
	if v := os.Getenv("SCANSTRESS_HARD_LIMIT_SEC"); v != "" {
		var n int
		if _, err := fmtSscan(v, &n); err == nil && n > 0 {
			hard = n
		}
	}
	scanner := newScanner(t, "stress-timeout", scancatalog.RuntimePolicy{SoftLimitSec: hard / 2, HardLimitSec: hard})
	project := writeCorpus(t, corpusSpec{Files: corpusFiles(), VulnPerFile: 3})

	obs := startObserver(t, 200*time.Millisecond)
	start := time.Now()
	_, err := scanner.Run(t.Context(), scan.ScanRequest{ProjectDir: project, Categories: []api.ScanCategory{api.ScanCategorySAST}})
	elapsed := time.Since(start)
	samples := obs.finish()

	if err == nil {
		t.Fatalf("scan completed in %s under a %ds hard limit; the limit did not bind", elapsed, hard)
	}
	// The engine must have been doing work when the limit fired.
	sawEngine := false
	for _, s := range samples {
		if s.EngineProcs > 0 {
			sawEngine = true
			break
		}
	}
	if !sawEngine {
		t.Fatal("hard limit fired before any engine process existed; the test measured startup, not a timeout")
	}
	overshoot := elapsed - time.Duration(hard)*time.Second
	if overshoot > 30*time.Second {
		t.Errorf("hard limit of %ds released after %s (overshoot %s)", hard, elapsed, overshoot)
	}
	drain, survivors := awaitNoEngineProcesses(t, 30*time.Second)
	if len(survivors) > 0 {
		t.Errorf("engine processes survived the hard limit after %s: %+v", drain, survivors)
	}
	owned := obs.seenRunDirs()
	if leaked := survivingDirs(owned); len(leaked) > 0 {
		t.Errorf("run directories leaked on the timeout path: %v", leaked)
	}
	reportf(t, "hard_limit_sec=%d elapsed=%s overshoot=%s drain=%s owned_run_dirs=%d err=%v",
		hard, elapsed.Round(time.Millisecond), overshoot.Round(time.Millisecond), drain.Round(time.Millisecond), len(owned), err)
}
