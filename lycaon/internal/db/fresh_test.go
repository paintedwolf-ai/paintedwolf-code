package db

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFreshEnabled(t *testing.T) {
	t.Setenv("LYCAON_DB_FRESH", "1")
	t.Setenv("LYCAON_CONFIG_DIR", "")
	t.Setenv("LYCAON_DEV", "")
	if FreshRequested() != true {
		t.Fatal("expected fresh requested")
	}
	if FreshEnabled() {
		t.Fatal("production channel must ignore LYCAON_DB_FRESH")
	}

	t.Setenv("LYCAON_DEV", "1")
	if !FreshEnabled() {
		t.Fatal("expected fresh enabled on the development channel")
	}

	t.Setenv("LYCAON_DEV", "")
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	if !FreshEnabled() {
		t.Fatal("expected fresh enabled under LYCAON_CONFIG_DIR override")
	}

	t.Setenv("LYCAON_DB_FRESH", "0")
	if FreshRequested() || FreshEnabled() {
		t.Fatal("expected fresh disabled")
	}
}

func TestRemoveStore(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "store.db")
	if err := os.WriteFile(dbPath, []byte("x"), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	for _, suffix := range StoreSidecarSuffixes() {
		if err := os.WriteFile(dbPath+suffix, []byte("y"), 0o600); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
	if err := RemoveStore(dbPath); err != nil {
		testutil.FailErr(t, "RemoveStore failed", err)
	}
	for _, suffix := range append([]string{""}, StoreSidecarSuffixes()...) {
		path := dbPath + suffix
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected removed %s err=%v", path, err)
		}
	}
}

func TestRemoveStoreThenOpenConsolidatedSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "store.db")
	sqlDB, err := Open(dbPath)
	testutil.FailErr(t, "Open failed", err)
	sqlDB.Close()

	if err := RemoveStore(dbPath); err != nil {
		testutil.FailErr(t, "RemoveStore failed", err)
	}
	sqlDB, err = Open(dbPath)
	testutil.FailErr(t, "Open after RemoveStore failed", err)
	defer sqlDB.Close()

	var indexCount int
	if err := sqlDB.QueryRowContext(t.Context(), `
		SELECT count(*) FROM sqlite_master
		WHERE type='index' AND name='idx_messages_workflow_run'`).Scan(&indexCount); err != nil {
		testutil.FailErr(t, "query index", err)
	}
	if indexCount != 1 {
		t.Fatalf("idx_messages_workflow_run count = %d want 1", indexCount)
	}
}
