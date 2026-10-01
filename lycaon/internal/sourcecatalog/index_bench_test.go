package sourcecatalog

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// BenchmarkIndexRepository measures cold discovery over a real repository:
// how long until the index covers the tree, how long until it answers at all,
// and what the generation costs in memory and on disk.
func BenchmarkIndexRepository(b *testing.B) {
	root := os.Getenv("PW_INDEX_BENCH_ROOT")
	if root == "" {
		b.Skip("set PW_INDEX_BENCH_ROOT to a repository")
	}
	b.Setenv("LYCAON_CONFIG_DIR", b.TempDir())
	c := New()
	b.Cleanup(func() {
		if err := c.Drain(context.Background()); err != nil {
			b.Error(err)
		}
	})
	started := time.Now()
	var peakHeap, peakSys uint64
	var firstAnswer time.Duration
	var reader *IndexReader
	for {
		r, status, err := c.OpenIndex(b.Context(), "benchmark", Root{ID: "root", Path: root}, 0)
		if err != nil {
			testutil.FailErr(b, "index benchmark", err)
		}
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		peakHeap = max(peakHeap, mem.HeapAlloc)
		peakSys = max(peakSys, mem.Sys)
		if r != nil {
			if firstAnswer == 0 {
				firstAnswer = time.Since(started)
			}
			if status.Complete && !status.Refreshing {
				reader = r
				break
			}
			_ = r.Close()
		}
		if status.State == StateFailed {
			b.Fatalf("discovery failed: %s", status.Error)
		}
		if time.Since(started) > 30*time.Minute {
			b.Fatal("discovery exceeded 30 minutes")
		}
		time.Sleep(100 * time.Millisecond)
	}
	cold := time.Since(started)
	defer func() { _ = reader.Close() }()
	var count int
	if err := reader.tx.QueryRowContext(b.Context(), "SELECT count(*) FROM nodes").Scan(&count); err != nil {
		testutil.FailErr(b, "index benchmark", err)
	}
	var stats runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&stats)
	bytes := treeStoreBytes(reader.store.file)
	b.Logf("index discovery: entries=%d first_answer=%s cold=%s heap=%d peak_heap=%d peak_runtime_sys=%d db_bytes=%d",
		count, firstAnswer, cold, stats.HeapAlloc, peakHeap, peakSys, bytes)
	b.ResetTimer()
	for b.Loop() {
		for _, name := range []string{"README.md", "moz.build", "NavigationTransition.cpp", "missing-file-index-test"} {
			if _, err := reader.MatchingFilePathsPage(b.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, name, FileBasenamePrefix, "", 2); err != nil {
				testutil.FailErr(b, "index benchmark", err)
			}
		}
	}
	b.ReportMetric(float64(count), "entries")
	b.ReportMetric(firstAnswer.Seconds(), "first_answer_seconds")
	b.ReportMetric(cold.Seconds(), "cold_seconds")
	b.ReportMetric(float64(peakHeap), "peak_heap_B")
	b.ReportMetric(float64(stats.HeapAlloc), "retained_heap_B")
	b.ReportMetric(float64(bytes), "disk_B")
}
