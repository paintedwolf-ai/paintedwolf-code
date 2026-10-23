//go:build scanstress

package scanstress

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestLargeOutputStaysBounded crosses console capture size with real findings.
func TestLargeOutputStaysBounded(t *testing.T) {
	const files, vulns = 24, 100
	scanner := newScanner(t, "stress-output", scancatalog.RuntimePolicy{})
	project := writeResourceCorpus(t, files, vulns)
	projectBytes := dirBytes(t, project)

	obs := startObserver(t, 250*time.Millisecond)
	sizes := startRunDirSizer(t, obs, 500*time.Millisecond)
	start := time.Now()
	result, err := scanner.Run(t.Context(), scan.ScanRequest{
		ProjectDir: project,
		Categories: []api.ScanCategory{api.ScanCategorySAST},
	})
	elapsed := time.Since(start)
	peakRunDir := sizes.finish()
	samples := obs.finish()
	if err != nil {
		t.Fatalf("dense-corpus scan: %v", err)
	}

	requireResourceFindings(t, result, files, vulns)
	requireResourceBounds(t, samples, 1)
	encoded, err := json.Marshal(result.Findings)
	if err != nil {
		t.Fatalf("encode resource findings: %v", err)
	}
	const reportBudget = 32 << 20
	if len(encoded) <= exec.DefaultMaxOutputBytes || len(encoded) > reportBudget || peakRunDir > reportBudget+projectBytes {
		t.Errorf("dense output must cross console capture within its fixture budget: findings=%d console_cap=%d run_dir=%d report_budget=%d", len(encoded), exec.DefaultMaxOutputBytes, peakRunDir, reportBudget)
	}
	top := peak(samples)
	reportf(t, "files=%d vulns_per_file=%d project_bytes=%d latency=%s findings=%d warnings=%d",
		files, vulns, projectBytes, elapsed.Round(time.Millisecond), result.FindingsCount, len(result.Warnings))
	reportf(t, "peak_run_dir_bytes=%d console_cap_bytes=%d peak_aggregate_rss_mib=%.1f peak_self_rss_mib=%.1f",
		peakRunDir, exec.DefaultMaxOutputBytes, float64(top.AggregateRSS)/1024, float64(top.SelfRSS)/1024)

	drain, survivors := awaitNoEngineProcesses(t, 30*time.Second)
	if len(survivors) > 0 {
		t.Errorf("engine processes survived a dense scan after %s: %+v", drain, survivors)
	}
	if leaked := survivingDirs(obs.seenRunDirs()); len(leaked) > 0 {
		t.Errorf("run directories leaked after a dense scan: %v", leaked)
	}
}

// runDirSizer samples the total bytes held in this process's engine run
// directories while a scan is in flight.
type runDirSizer struct {
	mu   sync.Mutex
	peak int64
	stop chan struct{}
	done chan struct{}
}

func startRunDirSizer(t *testing.T, obs *observer, interval time.Duration) *runDirSizer {
	t.Helper()
	s := &runDirSizer{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			var total int64
			for _, dir := range obs.seenRunDirs() {
				total += dirBytes(t, dir)
			}
			s.mu.Lock()
			if total > s.peak {
				s.peak = total
			}
			s.mu.Unlock()
			select {
			case <-s.stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return s
}

func (s *runDirSizer) finish() int64 {
	close(s.stop)
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak
}
