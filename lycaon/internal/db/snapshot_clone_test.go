package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A clone of a store whose log still holds committed frames is a single
// self-contained file with every committed row; where the volume cannot
// clone, nothing is left behind for the caller's fallback.
func TestCloneSnapshotFoldsTheLogIntoOneFile(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.db")
	live, err := sql.Open("sqlite", fileDSN(storePath, "_pragma=journal_mode(WAL)"))
	testutil.FailErr(t, "open live store", err)
	defer func() { _ = live.Close() }()
	live.SetMaxOpenConns(1)
	_, err = live.Exec(`PRAGMA wal_autocheckpoint=0; CREATE TABLE kept (v INTEGER); INSERT INTO kept VALUES (1), (2), (3); PRAGMA user_version=7`)
	testutil.FailErr(t, "write live store", err)
	if info, err := os.Stat(storePath + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("live log is empty; the test cannot show it folded: %v", err)
	}
	destPath := filepath.Join(dir, "snapshot", "store.db")
	testutil.FailErr(t, "create snapshot directory", os.Mkdir(filepath.Dir(destPath), 0o700))

	cloned, err := CloneSnapshot(t.Context(), live, storePath, destPath)
	testutil.FailErr(t, "clone snapshot", err)
	entries, err := os.ReadDir(filepath.Dir(destPath))
	testutil.FailErr(t, "list snapshot directory", err)
	if !cloned {
		if len(entries) != 0 {
			t.Fatalf("an unsupported clone left %d files behind", len(entries))
		}
		return
	}
	if len(entries) != 1 || entries[0].Name() != "store.db" {
		t.Fatalf("snapshot directory holds %v, want one store file", entries)
	}
	snapshot, err := sql.Open("sqlite", fileDSN(destPath, "mode=ro"))
	testutil.FailErr(t, "open snapshot", err)
	defer func() { _ = snapshot.Close() }()
	var rows, version int
	testutil.FailErr(t, "count snapshot rows", snapshot.QueryRow(`SELECT COUNT(*) FROM kept`).Scan(&rows))
	testutil.FailErr(t, "read snapshot version", snapshot.QueryRow(`PRAGMA user_version`).Scan(&version))
	if rows != 3 || version != 7 {
		t.Fatalf("snapshot rows=%d version=%d, want the committed log folded in", rows, version)
	}
}
