package db_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
)

func closedStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	store, err := db.OpenWithOptions(t.Context(), path, db.UpgradeHooks{}, db.StoreOptions{FastTestSync: true})
	testutil.FailErr(t, "open source store", err)
	_, err = store.ExecContext(t.Context(), `INSERT INTO store_meta(key, value) VALUES ('copy-probe', 'kept')`)
	testutil.FailErr(t, "write source row", err)
	testutil.FailErr(t, "close source store", store.Close())
	return path
}

func TestOpenCopyOfClosedStoreKeepsItsRows(t *testing.T) {
	src := closedStore(t)
	for _, name := range []string{"first.db", "second.db"} {
		copied, err := db.OpenCopy(t.Context(), src, filepath.Join(t.TempDir(), name), db.StoreOptions{FastTestSync: true})
		testutil.FailErr(t, "open copy", err)
		var value string
		testutil.FailErr(t, "read copied row", copied.QueryRowContext(t.Context(), `SELECT value FROM store_meta WHERE key = 'copy-probe'`).Scan(&value))
		if value != "kept" {
			t.Fatalf("copied value = %q", value)
		}
		testutil.FailErr(t, "close copy", copied.Close())
	}
}

func TestOpenCopyChecksASourceChangedAfterClose(t *testing.T) {
	src := closedStore(t)
	raw, err := sql.Open("sqlite", src)
	testutil.FailErr(t, "open source outside the store", err)
	_, err = raw.ExecContext(context.Background(), `ALTER TABLE store_meta ADD COLUMN injected TEXT`)
	testutil.FailErr(t, "change source schema", err)
	testutil.FailErr(t, "close raw source", raw.Close())

	_, err = db.OpenCopy(t.Context(), src, filepath.Join(t.TempDir(), "copy.db"), db.StoreOptions{FastTestSync: true})
	if !errors.Is(err, db.ErrStoreIncompatible) {
		t.Fatalf("copy of a changed source opened: %v", err)
	}
}
