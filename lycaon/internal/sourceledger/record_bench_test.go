package sourceledger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/pkg/api"
)

// Measures RecordBatch at promote-landing scale; every version insert
// advances the project's source-history clock.
func BenchmarkRecordBatchPromoteScale(b *testing.B) {
	for _, files := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("files-%d", files), func(b *testing.B) {
			sqlDB := testdbfixture.Open(b, "store.db")
			now := db.FormatTime(time.Now().UTC())
			if _, err := sqlDB.ExecContext(context.Background(), `
				INSERT INTO projects (id, name, last_opened_at, created_at, roots_generation)
				VALUES ('p1', NULL, ?, ?, 0)
			`, now, now); err != nil {
				b.Fatalf("insert project: %v", err)
			}
			if _, err := sqlDB.ExecContext(context.Background(), `
				INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at, kind)
				VALUES ('r1', 'p1', '/tmp/source-ledger-bench', 'source-ledger-bench', 1, ?, 'attached')
			`, now); err != nil {
				b.Fatalf("insert root: %v", err)
			}
			store := New(sqlDB, b.TempDir())
			ctx := context.Background()

			b.ResetTimer()
			for round := 0; b.Loop(); round++ {
				batch := make([]RecordInput, 0, files)
				for i := range files {
					content := fmt.Appendf(nil, "round %d file %d\n", round, i)
					batch = append(batch, RecordInput{
						RecordLocation: RecordLocation{RootID: "r1", Path: fmt.Sprintf("pkg/file%d.go", i)},
						ProjectID:      "p1",
						Op:             api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
						After: content})
				}
				if err := store.RecordBatch(ctx, batch); err != nil {
					b.Fatalf("record batch: %v", err)
				}
			}
			elapsed := b.Elapsed()
			if b.N > 0 {
				b.ReportMetric(float64(elapsed.Microseconds())/float64(b.N*files), "µs/file")
			}
		})
	}
}

// Measures what a program's first external launch waits for: the pass and the
// start retention a window open runs. Committed files need no retention.
func BenchmarkOpenCommandWindow(b *testing.B) {
	for _, tree := range []struct {
		files     int
		committed bool
	}{{2000, true}, {10000, true}, {200, false}} {
		for _, watched := range []bool{true, false} {
			b.Run(fmt.Sprintf("files-%d/committed-%t/watched-%t", tree.files, tree.committed, watched), func(b *testing.B) {
				benchmarkOpenCommandWindow(b, tree.files, tree.committed, watched)
			})
		}
	}
}

func benchmarkOpenCommandWindow(b *testing.B, files int, committed, watched bool) {
	if !watched {
		repochange.DisableWatchersForTest()
		b.Cleanup(repochange.ResetWatchersForTest)
	}
	sqlDB := testdbfixture.Open(b, "store.db")
	testdbseed.InsertProject(b, sqlDB, "p1")
	root := b.TempDir()
	for i := range files {
		dir := filepath.Join(root, fmt.Sprintf("pkg%d", i%50))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			b.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d.go", i)), fmt.Appendf(nil, "package p // %d\n", i), 0o640); err != nil {
			b.Fatalf("write: %v", err)
		}
	}
	if committed {
		gittestsetup.Enable()
		gittest.Init(b, root)
		gittest.CommitAll(b, root, "Tree")
	}
	if _, err := sqlDB.ExecContext(b.Context(), `INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at, kind)
		VALUES ('r1', 'p1', ?, 'root', 1, ?, 'attached')`, root, db.FormatTime(time.Now().UTC())); err != nil {
		b.Fatalf("insert root: %v", err)
	}
	store := New(sqlDB, b.TempDir())
	store.Commands.windowSettle = 0
	store.Commands.observationBudget = time.Minute
	if watched {
		repochange.MarkCoverageCompleteForTest(root)
		b.Cleanup(func() { repochange.CloseRoot(root) })
	}
	in := CommandWindowInput{ProjectID: "p1", Roots: []RootSpec{{ID: "r1", Path: root}}, SessionID: "s1", ToolName: "command", CommandLine: "make"}
	ctx := context.Background()
	openAndClose := func() {
		window, err := store.Commands.OpenCommandWindow(ctx, in)
		if err != nil {
			b.Fatalf("open: %v", err)
		}
		b.StopTimer()
		if err := window.Close(ctx); err != nil {
			b.Fatalf("close: %v", err)
		}
		<-window.Settled()
		b.StartTimer()
	}
	// The first open is the cold capture; a project pays it once.
	first := time.Now()
	openAndClose()
	firstOpen := time.Since(first)
	b.ResetTimer()
	for b.Loop() {
		openAndClose()
	}
	b.ReportMetric(float64(firstOpen.Milliseconds()), "first-cycle-ms")
}
