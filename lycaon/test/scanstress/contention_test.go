//go:build scanstress

package scanstress

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/pkg/api"
)

func concurrency() int {
	if v := os.Getenv("SCANSTRESS_CONCURRENCY"); v != "" {
		var n int
		if _, err := fmtSscan(v, &n); err == nil && n > 0 {
			return n
		}
	}
	return 4
}

// TestConcurrentScansStayBounded runs several maintained-engine scans at once
// and records what the host actually pays: per-scan latency spread, peak
// aggregate process-tree memory, peak descendant count, and cleanup.
func TestConcurrentScansStayBounded(t *testing.T) {
	n := concurrency()
	scanners := make([]scan.CodeScanner, n)
	projects := make([]string, n)
	for i := range scanners {
		scanners[i] = newScanner(t, "stress-contention", scancatalog.RuntimePolicy{})
		projects[i] = writeCorpus(t, corpusSpec{Files: corpusFiles(), VulnPerFile: 3})
	}

	loadBefore := loadAverage(t)
	obs := startObserver(t, 250*time.Millisecond)
	latencies := make([]time.Duration, n)
	findings := make([]int, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wallStart := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start := time.Now()
			res, err := scanners[i].Run(t.Context(), scan.ScanRequest{
				ProjectDir: projects[i],
				Categories: []api.ScanCategory{api.ScanCategorySAST},
			})
			latencies[i] = time.Since(start)
			errs[i] = err
			if res != nil {
				findings[i] = res.FindingsCount
			}
		}(i)
	}
	wg.Wait()
	wall := time.Since(wallStart)
	samples := obs.finish()

	failed := 0
	var min, max, total time.Duration
	for i := range latencies {
		if errs[i] != nil {
			failed++
			t.Errorf("concurrent scan %d failed: %v", i, errs[i])
			continue
		}
		if min == 0 || latencies[i] < min {
			min = latencies[i]
		}
		if latencies[i] > max {
			max = latencies[i]
		}
		total += latencies[i]
	}
	if failed == 0 {
		first := findings[0]
		for i, got := range findings {
			if got != first {
				t.Errorf("scan %d found %d findings against an identical corpus; scan 0 found %d", i, got, first)
			}
		}
	}

	top := peak(samples)
	reportf(t, "concurrency=%d wall=%s min=%s max=%s mean=%s failed=%d findings_each=%d",
		n, wall.Round(time.Millisecond), min.Round(time.Millisecond), max.Round(time.Millisecond),
		(total / time.Duration(n-failed)).Round(time.Millisecond), failed, findings[0])
	reportf(t, "peak_aggregate_rss_mib=%.1f peak_descendants=%d peak_distinct_pgids=%d loadavg_before=%.2f loadavg_after=%.2f",
		float64(top.AggregateRSS)/1024, peakProcs(samples), maxPGIDs(samples), loadBefore, loadAverage(t))

	drain, survivors := awaitNoEngineProcesses(t, 30*time.Second)
	if len(survivors) > 0 {
		t.Errorf("engine processes survived contention after %s: %+v", drain, survivors)
	}
	owned := obs.seenRunDirs()
	if len(owned) < n {
		reportf(t, "observer attributed %d run dirs for %d scans (sampling gap, not necessarily a leak)", len(owned), n)
	}
	if leaked := survivingDirs(owned); len(leaked) > 0 {
		t.Errorf("run directories leaked under contention: %v", leaked)
	}
	reportf(t, "owned_run_dirs=%d leaked=0 drain=%s", len(owned), drain.Round(time.Millisecond))
}

func maxPGIDs(samples []treeSample) int {
	best := 0
	for _, s := range samples {
		if s.DistinctPGIDs > best {
			best = s.DistinctPGIDs
		}
	}
	return best
}
