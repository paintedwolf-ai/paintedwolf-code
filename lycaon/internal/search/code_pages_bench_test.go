package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/scan/rules"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// BenchmarkPagedCodeSearch uses PW_SEARCH_BENCH_ROOT to measure an existing checkout read-only.
func BenchmarkPagedCodeSearch(b *testing.B) {
	b.Setenv("LYCAON_CONFIG_DIR", b.TempDir())
	root := os.Getenv("PW_SEARCH_BENCH_ROOT")
	if root == "" {
		root = b.TempDir()
		for i := range 4096 {
			if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("source-%04d.go", i)), []byte("package source\n// searchable content\n"), 0o600); err != nil {
				b.Fatalf("write fixture: %v", err)
			}
		}
	}
	catalog := sourcecatalog.New()
	b.Cleanup(func() {
		if err := catalog.Drain(context.Background()); err != nil {
			b.Errorf("drain indexing: %v", err)
		}
	})
	codeRoot := CodeRoot{ProjectID: "benchmark", RootID: "attached-root", Path: root}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	gen, err := resolveCodeGeneration(b.Context(), catalog, codeRoot, 10*time.Minute)
	if err != nil {
		b.Fatalf("discover metadata: %v", err)
	}
	files, err := gen.reader.FileCount(b.Context(), sourcecatalog.FileScope{Audience: sourcecatalog.HumanAudience, IncludeHidden: true})
	if err != nil {
		b.Fatalf("read totals: %v", err)
	}
	complete := gen.reader.Status.Complete
	_ = gen.reader.Close()
	discovery := time.Since(started)
	runtime.GC()
	runtime.ReadMemStats(&after)
	b.Logf("metadata: files=%d complete=%v discovery=%s retained_heap_delta=%d",
		files, complete, discovery, int64(after.HeapAlloc)-int64(before.HeapAlloc))
	executor := &CodeExecutor{catalog: catalog}
	excludes, err := rules.LoadPathExcludes()
	if err != nil {
		b.Fatalf("load search exclusions: %v", err)
	}
	for _, mode := range []struct {
		name   string
		lines  bool
		budget SearchBudget
		warm   bool
	}{{"files", false, BudgetInteractive, false}, {"interactive", true, BudgetInteractive, false}, {"complete", true, BudgetComplete, false}, {"complete_warm", true, BudgetComplete, true}} {
		b.Run(mode.name, func(b *testing.B) {
			if mode.warm {
				waitBenchmarkCodeIndex(b, executor, codeRoot, excludes.Patterns())
			}
			lineCap, fileCap := mode.budget.codeCaps()
			leg := PlanLeg{Code: &CodePlanLeg{Query: TextExpr{Text: "sql"}, PathRoots: []CodeRoot{codeRoot}, Files: true, Lines: mode.lines, Budget: mode.budget, FileCap: fileCap, Cap: lineCap, FileExcludeDirs: excludes.Patterns(), LineExcludeDirs: excludes.Patterns()}}
			for b.Loop() {
				report, runErr := executor.Run(b.Context(), leg)
				if runErr != nil {
					b.Fatalf("search: %v", runErr)
				}
				b.ReportMetric(float64(len(report.Hits)), "hits/op")
				b.ReportMetric(float64(report.Code.FilesOpened), "opened/op")
				if report.TimedOut {
					b.ReportMetric(1, "timed_out/op")
				}
			}
			b.ReportMetric(discovery.Seconds()*1000, "cold_discovery_ms")
			b.ReportMetric(float64(files), "catalog_files")
			b.ReportMetric(float64(int64(after.HeapAlloc)-int64(before.HeapAlloc)), "catalog_retained_B")
		})
	}
}

func waitBenchmarkCodeIndex(b *testing.B, executor *CodeExecutor, root CodeRoot, excludes []string) {
	b.Helper()
	ctx, cancel := context.WithTimeout(b.Context(), 5*time.Minute)
	defer cancel()
	for {
		report, err := executor.Run(ctx, PlanLeg{Code: &CodePlanLeg{Query: TextExpr{Text: "sql"}, PathRoots: []CodeRoot{root}, Lines: true, Cap: 17, Budget: BudgetInteractive, LineExcludeDirs: excludes}})
		if err != nil {
			b.Fatalf("prepare content observations: %v", err)
		}
		if report.Code.IndexWarmingRoots == 0 && report.Code.WarmingRoots == 0 {
			return
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			b.Fatalf("content preparation: %v", ctx.Err())
		case <-timer.C:
		}
	}
}
