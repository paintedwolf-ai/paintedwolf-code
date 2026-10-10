package webindex

import (
	"context"
	"database/sql"
	"errors"
	"github.com/lycaon/lycaon/internal/testutil"
	"os"
	"path/filepath"
	"testing"
)

func TestSchemaVersionBumpWipes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-index.db")
	s, err := Open(t.Context(), path)
	testutil.FailErr(t, "open store", err)
	s.QueuePage(t.Context(), Page{URL: "https://a.example/p", Title: "steam machine"})
	s.Flush()
	testutil.FailErr(t, "close store", s.Close())

	db, err := openFile(t.Context(), path)
	testutil.FailErr(t, "reopen raw", err)
	_, err = db.Exec("PRAGMA user_version = 99")
	testutil.FailErr(t, "bump version", err)
	testutil.FailErr(t, "close raw", db.Close())

	s2, err := Open(t.Context(), path)
	testutil.FailErr(t, "reopen store", err)
	t.Cleanup(func() { _ = s2.Close() })
	docs, err := s2.Search(context.Background(), "steam machine", 10)
	testutil.FailErr(t, "search", err)
	if len(docs) != 0 {
		t.Fatalf("docs = %+v want wiped index on version mismatch", docs)
	}
}

func TestCanceledOpenPreservesMismatchedCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-index.db")
	store, err := Open(t.Context(), path)
	testutil.FailErr(t, "open cache", err)
	store.QueuePage(t.Context(), Page{URL: "https://a.example/retained", Title: "retained evidence"})
	store.Flush()
	testutil.FailErr(t, "close cache", store.Close())
	database, err := sql.Open("sqlite", "file:"+path)
	testutil.FailErr(t, "inspect cache", err)
	_, err = database.ExecContext(t.Context(), "PRAGMA user_version = 99")
	testutil.FailErr(t, "stamp old schema", err)
	testutil.FailErr(t, "close old schema", database.Close())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	canceled, err := Open(ctx, path)
	if canceled != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled open = %v, %v", canceled, err)
	}
	database, err = sql.Open("sqlite", "file:"+path)
	testutil.FailErr(t, "reopen retained cache", err)
	defer func() { _ = database.Close() }()
	var revision, retained int
	testutil.FailErr(t, "read retained schema", database.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&revision))
	testutil.FailErr(t, "read retained page", database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM docs WHERE url = ?", "https://a.example/retained").Scan(&retained))
	if revision != 99 || retained != 1 {
		t.Fatalf("canceled startup changed cache: schema=%d retained=%d", revision, retained)
	}
}

func TestOpenDoesNotReplaceBlockingParentFile(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "retained")
	if err := os.WriteFile(parent, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), filepath.Join(parent, "web-index.db"))
	if store != nil || err == nil {
		t.Fatalf("open=%v error=%v", store, err)
	}
	raw, err := os.ReadFile(parent)
	if err != nil || string(raw) != "keep" {
		t.Fatalf("failed startup changed parent: %q %v", raw, err)
	}
}
