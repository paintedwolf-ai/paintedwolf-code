package sourceobservation

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestClearDropsAllRebuildableObservations(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "source-observations.db"))
	t.Cleanup(func() { _ = store.Close() })
	testutil.FailErr(t, "put first root", store.Put(t.Context(), "/project/a", []File{{
		Path: "a.go", SHA256: "aaa", GitOID: "111", Size: 3, Mode: 0o644, ModifiedNS: 1,
	}}))
	testutil.FailErr(t, "put second root", store.Put(t.Context(), "/project/b", []File{{
		Path: "b.go", SHA256: "bbb", Size: 3, Mode: 0o644, ModifiedNS: 2,
	}}))
	file, ok, err := store.Get(t.Context(), "/project/a", "a.go")
	testutil.FailErr(t, "get before clear", err)
	if !ok || file.GitOID != "111" {
		t.Fatalf("observation before clear = %+v ok=%v", file, ok)
	}
	testutil.FailErr(t, "clear", store.Clear(t.Context()))
	for _, root := range []string{"/project/a", "/project/b"} {
		_, ok, err := store.Get(t.Context(), root, filepath.Base(root)+".go")
		testutil.FailErr(t, "get "+root, err)
		if ok {
			t.Fatalf("observation for %s survived clear", root)
		}
	}
}

func TestReaderAnswersPointLookups(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "source-observations.db"))
	t.Cleanup(func() { _ = store.Close() })
	testutil.FailErr(t, "put", store.Put(t.Context(), "/project", []File{{
		Path: "a.go", SHA256: "aaa", Size: 3, Mode: 0o644, ModifiedNS: 1,
	}}))
	reader, err := store.Reader(t.Context())
	testutil.FailErr(t, "prepare reader", err)
	t.Cleanup(func() { _ = reader.Close() })
	file, ok, err := reader.Get(t.Context(), "/project", "a.go")
	testutil.FailErr(t, "read", err)
	if !ok || file.SHA256 != "aaa" {
		t.Fatalf("reader observation = %+v ok=%v", file, ok)
	}
	if _, ok, err := reader.Get(t.Context(), "/project", "missing.go"); err != nil || ok {
		t.Fatalf("missing path ok=%v err=%v", ok, err)
	}
}

func TestOpenRebuildsMismatchedCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source-observations.db")
	store := New(path)
	testutil.FailErr(t, "put observation", store.Put(t.Context(), "/project", []File{{
		Path: "a.go", SHA256: "aaa", Size: 3, Mode: 0o644, ModifiedNS: 1,
	}}))
	testutil.FailErr(t, "close observation store", store.Close())

	database, err := sql.Open("sqlite", "file:"+path)
	testutil.FailErr(t, "open raw cache", err)
	_, err = database.Exec(`PRAGMA user_version = 9`)
	testutil.FailErr(t, "change cache version", err)
	testutil.FailErr(t, "close raw cache", database.Close())

	store = New(path)
	t.Cleanup(func() { _ = store.Close() })
	_, ok, err := store.Get(t.Context(), "/project", "a.go")
	testutil.FailErr(t, "read rebuilt cache", err)
	if ok {
		t.Fatal("rebuilt cache still holds the old observation")
	}
}

func TestOpenRebuildsUnreadableCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source-observations.db")
	testutil.FailErr(t, "write unreadable cache", os.WriteFile(path, []byte("not sqlite"), 0o600))

	store := New(path)
	t.Cleanup(func() { _ = store.Close() })
	_, ok, err := store.Get(t.Context(), "/project", "a.go")
	testutil.FailErr(t, "read rebuilt cache", err)
	if ok {
		t.Fatal("rebuilt cache holds an observation")
	}
}
