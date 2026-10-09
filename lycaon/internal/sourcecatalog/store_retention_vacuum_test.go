package sourcecatalog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestVacuumPreservesIdleStoreLastUse(t *testing.T) {
	catalog := treeTestCatalog(t)
	old := filepath.Join(catalog.Trees.treeDir, "z-old.db")
	recent := filepath.Join(catalog.Trees.treeDir, "a-recent.db")
	for _, file := range []string{old, recent} {
		database, err := openTreeDB(t.Context(), file)
		testutil.FailErr(t, "open retained store", err)
		_, err = database.ExecContext(t.Context(), "CREATE TABLE fixture(body BLOB); INSERT INTO fixture VALUES(zeroblob(1048576)); PRAGMA wal_checkpoint(TRUNCATE)")
		testutil.FailErr(t, "populate retained store", err)
		if file == old {
			_, err = database.ExecContext(t.Context(), "DELETE FROM fixture; PRAGMA wal_checkpoint(TRUNCATE)")
			testutil.FailErr(t, "free old store pages", err)
		}
		testutil.FailErr(t, "close retained store", database.Close())
	}
	lastUsed := time.Now().Add(-time.Hour)
	testutil.FailErr(t, "mark old store idle", os.Chtimes(old, lastUsed, lastUsed))
	lastUsed = lastTreeUse(old)
	_, err := catalog.Trees.reconcileTreeStores(t.Context(), treeStorePolicy{retention: 24 * time.Hour, maxBytes: 1 << 30, vacuumPages: 2048})
	testutil.FailErr(t, "reclaim idle stores without eviction", err)
	if got := lastTreeUse(old); !got.Equal(lastUsed) {
		t.Fatalf("vacuum renewed idle store use: got %s, want %s", got, lastUsed)
	}
	removed, err := catalog.Trees.reconcileTreeStores(t.Context(), treeStorePolicy{retention: 24 * time.Hour, maxBytes: treeStoreBytes(recent), vacuumPages: 2048})
	testutil.FailErr(t, "reconcile old and recent stores", err)
	if removed != 1 {
		t.Fatalf("removed=%d, want one oldest idle store", removed)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("vacuum changed eviction order: old store still exists: %v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("recent store removed before vacuumed old store: %v", err)
	}
}
