package sourcesnapshot

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
)

type heldSweepWriter struct {
	db.Handle
	entered chan struct{}
	proceed chan struct{}
}

func (d heldSweepWriter) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	close(d.entered)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-d.proceed:
		return d.Handle.BeginTx(ctx, opts)
	}
}

func TestSweepWriterWaitDoesNotFreezeCapturePins(t *testing.T) {
	s := openSnapshotStore(t)
	writer := heldSweepWriter{Handle: s.db, entered: make(chan struct{}), proceed: make(chan struct{})}
	collector := snapshotCollector{db: writer, queries: s.queries, pins: s.pins}
	done := make(chan error, 1)
	go func() {
		done <- collector.inPinnedTx(t.Context(), func(_ *db.Queries, manifestPinned, _ func(string) bool) error {
			if !manifestPinned("capture") {
				t.Error("capture pin was not visible to collection")
			}
			return nil
		})
	}()
	<-writer.entered
	held := s.pins.holdManifests("capture")
	defer held.release()
	close(writer.proceed)
	testutil.FailErr(t, "collect after writer admission", <-done)
}
