package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A store this process cannot open at all needs the recovery surface, not a
// plain error that renders as an offline card whose only action is retry.
// SQLITE_CANTOPEN is the code an unreadable database or journal reports, and it
// is exactly what an engine killed mid-shutdown can leave behind.
func TestOpenRoutesUnopenableStoresIntoRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	testutil.FailErr(t, "seed unopenable store", os.Mkdir(path, 0o700))

	_, err := Open(path)
	if err == nil {
		t.Fatal("opening a store that is not a file must fail")
	}
	if !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("open error = %v, want ErrStoreIncompatible so the boot reaches recovery mode", err)
	}
}

// Development changes can share the revision marker; every gate checks shape.
func TestCheckBaselineRejectsAMovedShapeUnderTheFixedMarker(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	store, err := Open(path)
	testutil.FailErr(t, "open store", err)
	testutil.FailErr(t, "shutdown store", store.Shutdown(ctx))

	moveStoreShape(t, path)

	sqlDB, err := sql.Open("sqlite", "file:"+path)
	testutil.FailErr(t, "reopen store", err)
	defer func() { _ = sqlDB.Close() }()

	version, err := ReadUserVersion(ctx, sqlDB)
	testutil.FailErr(t, "read user_version", err)
	if version != SchemaVersion {
		t.Fatalf("moving the shape changed the marker to %d; the fixture no longer tests shape", version)
	}

	err = CheckBaseline(ctx, sqlDB)
	if !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("CheckBaseline = %v, want ErrStoreIncompatible for a store whose structure moved", err)
	}
}

func TestShapeDigestSeparatesAMovedShapeFromTheBaseline(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	store, err := Open(path)
	testutil.FailErr(t, "open store", err)
	testutil.FailErr(t, "shutdown store", store.Shutdown(ctx))

	sqlDB, err := sql.Open("sqlite", "file:"+path)
	testutil.FailErr(t, "reopen store", err)
	defer func() { _ = sqlDB.Close() }()

	baseline, err := BaselineShapeDigest(ctx)
	testutil.FailErr(t, "baseline digest", err)
	live, err := ShapeDigest(ctx, sqlDB)
	testutil.FailErr(t, "live digest", err)
	if live != baseline {
		t.Fatalf("a fresh store digest %s does not match the baseline %s", live, baseline)
	}

	moveStoreShape(t, path)
	moved, err := ShapeDigest(ctx, sqlDB)
	testutil.FailErr(t, "moved digest", err)
	if moved == baseline {
		t.Fatal("shape digest did not change when the store structure moved")
	}
}

// moveStoreShape adds an index the baseline does not declare, leaving the fixed
// user_version marker untouched.
func moveStoreShape(t *testing.T, path string) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", "file:"+path)
	testutil.FailErr(t, "open store for shape change", err)
	defer func() { _ = sqlDB.Close() }()
	_, err = sqlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_store_meta_shape_probe ON store_meta(value)`)
	testutil.FailErr(t, "add index", err)
}
