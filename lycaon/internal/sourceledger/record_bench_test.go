package sourceledger

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
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
						ProjectID: "p1", RootID: "r1", Path: fmt.Sprintf("pkg/file%d.go", i),
						Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
						After: content,
					})
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
