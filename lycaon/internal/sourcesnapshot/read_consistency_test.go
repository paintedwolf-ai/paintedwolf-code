package sourcesnapshot

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
)

type deleteDuringSnapshotRead struct {
	db.Handle
	remove func()
}

func (h deleteDuringSnapshotRead) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	row := h.Handle.QueryRowContext(ctx, query, args...)
	h.remove()
	return row
}

func (h deleteDuringSnapshotRead) BeginReadTx(ctx context.Context) (*sql.Tx, error) {
	tx, err := h.Handle.(db.ReadHandle).BeginReadTx(ctx)
	if err != nil {
		return nil, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM source_snapshots`).Scan(&count); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	h.remove()
	return tx, nil
}

func TestSnapshotMetadataRemainsConsistentDuringDeletion(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "kept.go", "package kept\n")
	snapshot, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	testutil.FailErr(t, "capture snapshot", err)
	original := store.db
	handle := deleteDuringSnapshotRead{Handle: original, remove: func() {
		_, err := original.ExecContext(t.Context(), `DELETE FROM source_snapshot_heads WHERE snapshot_id = ?`, snapshot.ID)
		testutil.FailErr(t, "delete head", err)
		_, err = original.ExecContext(t.Context(), `DELETE FROM source_snapshots WHERE id = ?`, snapshot.ID)
		testutil.FailErr(t, "delete snapshot", err)
	}}
	store.db, store.queries = handle, db.New(handle)
	got, err := store.Get(t.Context(), snapshot.ID)
	testutil.FailErr(t, "read snapshot during deletion", err)
	if got.FileCount != 1 || len(got.Roots) != 1 || got.Roots[0].Path != root {
		t.Fatalf("snapshot read mixed states: %+v", got)
	}
}
