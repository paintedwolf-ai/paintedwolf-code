package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
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

func TestQuiescentCopyPreservesCommittedWALAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
	writer, err := sql.Open("sqlite", fileDSN(path, "_pragma=journal_mode(WAL)"))
	testutil.FailErr(t, "open source writer", err)
	defer func() { _ = writer.Close() }()
	_, err = writer.Exec(`PRAGMA wal_autocheckpoint=0; CREATE TABLE retained(v TEXT); INSERT INTO retained VALUES('committed'); PRAGMA user_version=7`)
	testutil.FailErr(t, "write committed history", err)
	source, err := openReader(t.Context(), path)
	testutil.FailErr(t, "open quiescent reader", err)
	defer func() { _ = source.Close() }()
	destination := filepath.Join(t.TempDir(), "copy.db")
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := CopyQuiescentSnapshot(canceled, source, path, destination); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled copy = %v", err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("canceled copy published a destination: %v", err)
	}
	sum, err := CopyQuiescentSnapshot(t.Context(), source, path, destination)
	testutil.FailErr(t, "copy quiescent history", err)
	raw, err := os.ReadFile(destination)
	testutil.FailErr(t, "read copied bytes", err)
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != sum {
		t.Fatalf("copied digest = %s, want %s", got, sum)
	}
	snapshot, err := openReader(t.Context(), destination)
	testutil.FailErr(t, "open copied history", err)
	defer func() { _ = snapshot.Close() }()
	for name, database := range map[string]*sql.DB{"source": source, "snapshot": snapshot} {
		var value string
		testutil.FailErr(t, "read "+name, database.QueryRow(`SELECT v FROM retained`).Scan(&value))
		if value != "committed" {
			t.Fatalf("%s history = %q", name, value)
		}
	}
	info, err := os.Stat(path + "-wal")
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("inspect source WAL: %v", err)
	}
	if err == nil && info.Size() != 0 {
		t.Fatalf("checkpoint left %d WAL bytes", info.Size())
	}
}

func TestQuiescentCopyRequiresSource(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "copy.db")
	if _, err := CopyQuiescentSnapshot(t.Context(), nil, "", destination); err == nil {
		t.Fatal("copy accepted a missing database")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("invalid copy published a destination: %v", err)
	}
}
