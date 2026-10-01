package db

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/version"
)

func TestOpenFreshStampsAppVersion(t *testing.T) {
	ctx := t.Context()
	SetRunningAppVersion(version.Version)
	dbPath := filepath.Join(t.TempDir(), "store.db")
	sqlDB, err := Open(dbPath)
	testutil.FailErr(t, "Open", err)
	defer sqlDB.Close()

	got, ok, err := ReadAppVersion(ctx, sqlDB)
	testutil.FailErr(t, "ReadAppVersion", err)
	if !ok || got != version.Version {
		t.Fatalf("app_version = %q ok=%v want %q", got, ok, version.Version)
	}
	if _, ok, err := ReadBootPreviousAppVersion(ctx, sqlDB); err != nil {
		testutil.FailErr(t, "ReadBootPreviousAppVersion", err)
	} else if ok {
		t.Fatal("fresh store must not set boot_previous_app_version")
	}
}

func TestOpenUnchangedAppVersionIsNoOp(t *testing.T) {
	ctx := t.Context()
	SetRunningAppVersion(version.Version)
	dbPath := filepath.Join(t.TempDir(), "store.db")
	sqlDB, err := Open(dbPath)
	testutil.FailErr(t, "Open", err)
	testutil.FailErr(t, "close", sqlDB.Close())

	// An unchanged boot clears the transition marker.
	sqlDB, err = Open(dbPath)
	testutil.FailErr(t, "reopen", err)
	testutil.FailErr(t, "force previous", New(sqlDB).UpsertStoreMeta(ctx, UpsertStoreMetaParams{
		Key:   bootPreviousAppVersionMetaKey,
		Value: "0.0.1",
	}))
	testutil.FailErr(t, "close with pending", sqlDB.Close())

	sqlDB, err = Open(dbPath)
	testutil.FailErr(t, "unchanged reopen", err)
	defer sqlDB.Close()

	got, ok, err := ReadAppVersion(ctx, sqlDB)
	testutil.FailErr(t, "ReadAppVersion", err)
	if !ok || got != version.Version {
		t.Fatalf("app_version = %q ok=%v want %q", got, ok, version.Version)
	}
	if _, ok, err := ReadBootPreviousAppVersion(ctx, sqlDB); err != nil {
		testutil.FailErr(t, "ReadBootPreviousAppVersion", err)
	} else if ok {
		t.Fatal("unchanged boot must clear boot_previous_app_version")
	}
}

func TestOpenChangedAppVersionRecordsTransition(t *testing.T) {
	ctx := t.Context()
	SetRunningAppVersion(version.Version)
	dbPath := filepath.Join(t.TempDir(), "store.db")
	sqlDB, err := Open(dbPath)
	testutil.FailErr(t, "Open", err)
	testutil.FailErr(t, "stamp other", New(sqlDB).UpsertStoreMeta(ctx, UpsertStoreMetaParams{
		Key:   appVersionMetaKey,
		Value: "0.0.1",
	}))
	testutil.FailErr(t, "close", sqlDB.Close())

	sqlDB, err = Open(dbPath)
	testutil.FailErr(t, "upgrade Open", err)
	defer sqlDB.Close()

	got, ok, err := ReadAppVersion(ctx, sqlDB)
	testutil.FailErr(t, "ReadAppVersion", err)
	if !ok || got != version.Version {
		t.Fatalf("app_version = %q ok=%v want %q", got, ok, version.Version)
	}
	prev, ok, err := ReadBootPreviousAppVersion(ctx, sqlDB)
	testutil.FailErr(t, "ReadBootPreviousAppVersion", err)
	if !ok || prev != "0.0.1" {
		t.Fatalf("boot_previous_app_version = %q ok=%v want 0.0.1", prev, ok)
	}
}

func TestOpenAbsentAppVersionInitializesMetadata(t *testing.T) {
	ctx := t.Context()
	SetRunningAppVersion(version.Version)
	dbPath := filepath.Join(t.TempDir(), "store.db")
	sqlDB, err := Open(dbPath)
	testutil.FailErr(t, "Open", err)
	testutil.FailErr(t, "delete app_version", New(sqlDB).DeleteStoreMeta(ctx, appVersionMetaKey))
	testutil.FailErr(t, "close", sqlDB.Close())

	sqlDB, err = Open(dbPath)
	testutil.FailErr(t, "Open after delete", err)
	defer sqlDB.Close()
	got, ok, err := ReadAppVersion(ctx, sqlDB)
	testutil.FailErr(t, "ReadAppVersion", err)
	if !ok || got != version.Version {
		t.Fatalf("app_version = %q ok=%v want %q", got, ok, version.Version)
	}
}

func TestRecordAppVersionTransitionAtomic(t *testing.T) {
	ctx := t.Context()
	sqlDB := openTestDB(t)
	testutil.FailErr(t, "seed current", RecordAppVersionTransition(ctx, sqlDB, "0.0.1", "", false))

	// Fail the second metadata write so neither key can commit.
	_, err := sqlDB.ExecContext(ctx, `
CREATE TRIGGER fail_boot_prev BEFORE INSERT ON store_meta
WHEN NEW.key = 'boot_previous_app_version'
BEGIN
  SELECT RAISE(ABORT, 'injected failure');
END;
`)
	testutil.FailErr(t, "install fault trigger", err)

	err = RecordAppVersionTransition(ctx, sqlDB, version.Version, "0.0.1", true)
	if err == nil {
		t.Fatal("expected transition to fail under fault injection")
	}
	got, ok, err := ReadAppVersion(ctx, sqlDB)
	testutil.FailErr(t, "ReadAppVersion after fault", err)
	if !ok || got != "0.0.1" {
		t.Fatalf("app_version = %q ok=%v want rolled-back 0.0.1", got, ok)
	}
	if _, ok, err := ReadBootPreviousAppVersion(ctx, sqlDB); err != nil {
		testutil.FailErr(t, "ReadBootPreviousAppVersion after fault", err)
	} else if ok {
		t.Fatal("boot_previous_app_version must not survive a rolled-back transition")
	}
}
