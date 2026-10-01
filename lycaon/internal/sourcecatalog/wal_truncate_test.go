package sourcecatalog

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A reader pinned to an earlier generation defers truncation; it must not
// hold the writer lock against every writer until the busy timeout.
func TestTruncateTreeWALDoesNotStallWritersBehindAPinnedReader(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "tree"+treeFileSuffix)
	db, err := openTreeDB(ctx, file)
	testutil.FailErr(t, "open tree store", err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(ctx, "CREATE TABLE rows_written (v INTEGER); INSERT INTO rows_written VALUES (1)")
	testutil.FailErr(t, "seed", err)

	reader, err := openTreeDB(ctx, file)
	testutil.FailErr(t, "open reader", err)
	t.Cleanup(func() { _ = reader.Close() })
	pinned, err := reader.BeginTx(ctx, nil)
	testutil.FailErr(t, "pin reader", err)
	t.Cleanup(func() { _ = pinned.Rollback() })
	var n int
	testutil.FailErr(t, "read", pinned.QueryRowContext(ctx, "SELECT count(*) FROM rows_written").Scan(&n))
	_, err = db.ExecContext(ctx, "INSERT INTO rows_written VALUES (2)")
	testutil.FailErr(t, "write past the pinned snapshot", err)

	started := time.Now()
	truncateTreeWAL(ctx, file)
	if waited := time.Since(started); waited > 2*time.Second {
		t.Fatalf("checkpoint waited %s for the pinned reader", waited)
	}

	writer, err := openTreeDB(ctx, file)
	testutil.FailErr(t, "open writer", err)
	t.Cleanup(func() { _ = writer.Close() })
	_, err = writer.ExecContext(ctx, "PRAGMA busy_timeout=100")
	testutil.FailErr(t, "shorten writer wait", err)
	_, err = writer.ExecContext(ctx, "INSERT INTO rows_written VALUES (3)")
	testutil.FailErr(t, "write after the checkpoint", err)
}
