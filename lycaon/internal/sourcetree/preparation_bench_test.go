package sourcetree

import (
	"context"
	"os"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

// BenchmarkRecursivePreparation isolates cold expansion from search enrichment.
func BenchmarkRecursivePreparation(b *testing.B) {
	root := os.Getenv("PW_TREE_BENCH_ROOT")
	if root == "" {
		b.Skip("set PW_TREE_BENCH_ROOT to a repository")
	}
	if profile := os.Getenv("PW_TREE_BENCH_PROFILE"); profile != "" {
		file, err := os.Create(profile)
		testutil.FailErr(b, "create preparation profile", err)
		defer func() { testutil.FailErr(b, "close preparation profile", file.Close()) }()
		testutil.FailErr(b, "start preparation profile", pprof.StartCPUProfile(file))
		defer pprof.StopCPUProfile()
	}
	for _, inventory := range []bool{false, true} {
		name := "alone"
		if inventory {
			name = "with_inventory"
		}
		b.Run(name, func(b *testing.B) {
			var preparation time.Duration
			var rows int64
			for b.Loop() {
				b.StopTimer()
				b.Setenv("LYCAON_CONFIG_DIR", b.TempDir())
				b.StartTimer()
				elapsed, count := benchmarkRecursivePreparation(b, root, inventory)
				preparation += elapsed
				rows = count
			}
			b.ReportMetric(preparation.Seconds()/float64(b.N), "preparation_seconds")
			b.ReportMetric(float64(rows), "rows")
		})
	}
}

func benchmarkRecursivePreparation(b *testing.B, directory string, inventory bool) (time.Duration, int64) {
	b.Helper()
	catalog := sourcecatalog.New()
	defer func() { testutil.FailErr(b, "drain catalog", catalog.Drain(context.Background())) }()
	root := sourcecatalog.Root{ID: "root", Path: directory}
	view := New(b.Context(), pagedview.Scope{Person: "person", Project: "project", Workspace: "workspace"},
		[]Root{{Root: root, Label: "Source"}}, catalog, nil)
	defer view.Close()
	started := time.Now()
	if inventory {
		testutil.FailErr(b, "start inventory", catalog.WarmNavigation(b.Context(), "project", root))
	}
	testutil.FailErr(b, "expand root", view.Disclose(b.Context(), nil, IntentEntry{
		Address: Address{Root: root.ID, Path: "."}, Disclosure: Disclosure{Open: true, Recursive: true}}))
	<-view.Prepare()
	presentation, err := view.Capture(b.Context())
	testutil.FailErr(b, "capture expanded tree", err)
	defer presentation.Close()
	frame, err := presentation.Frame(b.Context(), FrameRequest{Limit: 200})
	testutil.FailErr(b, "read first expanded viewport", err)
	return time.Since(started), frame.Extent.Rows
}
