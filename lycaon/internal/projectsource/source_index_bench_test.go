package projectsource

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

func BenchmarkSourceIndexSearch(b *testing.B) {
	b.Setenv("LYCAON_CONFIG_DIR", b.TempDir())
	root := os.Getenv("PW_SEARCH_BENCH_ROOT")
	if root == "" {
		root = b.TempDir()
		for i := range 4096 {
			if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%04d.sql", i)), []byte("source"), 0o600); err != nil {
				b.Fatalf("write fixture: %v", err)
			}
		}
	}
	catalog := sourcecatalog.New()
	b.Cleanup(func() {
		if err := catalog.Drain(context.Background()); err != nil {
			b.Errorf("drain catalog: %v", err)
		}
	})
	p := &Project{ID: "benchmark", Roots: []Root{{ID: "root", Path: root}}}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	for {
		r, status, err := catalog.Trees.OpenIndex(b.Context(), p.ID, sourcecatalog.Root{ID: "root", Path: root}, 10*time.Minute)
		if err != nil || r == nil {
			b.Fatalf("discover source paths: status=%+v err=%v", status, err)
		}
		_ = r.Close()
		if status.Complete && !status.Refreshing {
			break
		}
	}
	discovery := time.Since(started)
	runtime.GC()
	runtime.ReadMemStats(&after)
	cache := &SourceIndexCache{catalog: catalog}
	files := 0
	for b.Loop() {
		view := cache.Snapshot(b.Context(), p)
		files = view.FileCount
		matches, err := SearchSourceIndex(b.Context(), view, SourceQuery{Path: "sql"}, SourcePathStyle{}, "", nil, 50)
		view.Close()
		if err != nil || len(matches) == 0 {
			b.Fatalf("search paths: hits=%d err=%v", len(matches), err)
		}
	}
	b.ReportMetric(discovery.Seconds()*1000, "cold_discovery_ms")
	b.ReportMetric(float64(files), "catalog_files")
	b.ReportMetric(float64(int64(after.HeapAlloc)-int64(before.HeapAlloc)), "catalog_retained_B")
}
