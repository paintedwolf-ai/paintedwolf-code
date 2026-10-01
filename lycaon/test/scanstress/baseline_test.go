//go:build scanstress

package scanstress

import (
	"os"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/pkg/api"
)

// corpusFiles sizes every stress corpus. A larger corpus lengthens the engine
// phase so cancellation and shutdown land inside real work, not startup.
func corpusFiles() int {
	if v := os.Getenv("SCANSTRESS_FILES"); v != "" {
		var n int
		if _, err := fmtSscan(v, &n); err == nil && n > 0 {
			return n
		}
	}
	return 240
}

// TestBaselineScanCost measures one uncontended scan: wall latency, aggregate
// process-tree memory, report bytes, and post-run cleanup.
func TestBaselineScanCost(t *testing.T) {
	scanner := newScanner(t, "stress-baseline", scancatalog.RuntimePolicy{})
	project := writeCorpus(t, corpusSpec{Files: corpusFiles(), VulnPerFile: 3})

	loadBefore := loadAverage(t)
	obs := startObserver(t, 250*time.Millisecond)
	start := time.Now()
	result, err := scanner.Run(t.Context(), scan.ScanRequest{
		ProjectDir: project,
		Categories: []api.ScanCategory{api.ScanCategorySAST},
	})
	elapsed := time.Since(start)
	samples := obs.finish()
	if err != nil {
		t.Fatalf("baseline scan: %v", err)
	}

	top := peak(samples)
	reportf(t, "files=%d latency=%s findings=%d warnings=%d scanned_paths=%d loadavg_before=%.2f loadavg_after=%.2f",
		corpusFiles(), elapsed.Round(time.Millisecond), result.FindingsCount, len(result.Warnings),
		len(result.ScannedPaths), loadBefore, loadAverage(t))
	reportf(t, "peak_aggregate_rss_mib=%.1f peak_self_rss_mib=%.1f peak_descendants=%d samples=%d",
		float64(top.AggregateRSS)/1024, float64(top.SelfRSS)/1024, peakProcs(samples), len(samples))

	drain, survivors := awaitNoEngineProcesses(t, 10*time.Second)
	if len(survivors) > 0 {
		t.Errorf("engine processes survived a completed scan after %s: %+v", drain, survivors)
	}
	owned := obs.seenRunDirs()
	if len(owned) == 0 {
		t.Fatal("observer never attributed a run directory to this process")
	}
	if leaked := survivingDirs(owned); len(leaked) > 0 {
		t.Errorf("run directories leaked on the success path: %v", leaked)
	}
	reportf(t, "owned_run_dirs=%d leaked=%d engine_drain=%s", len(owned), len(survivingDirs(owned)), drain.Round(time.Millisecond))
}
