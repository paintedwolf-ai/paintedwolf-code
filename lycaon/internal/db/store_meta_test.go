package db

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBumpStoreRevisionMonotonic(t *testing.T) {
	ctx := context.Background()
	sqlDB := openTestDB(t)

	first, err := BumpStoreRevision(ctx, sqlDB)
	testutil.FailErr(t, "BumpStoreRevision first", err)
	if first != 1 {
		t.Fatalf("first revision = %d, want 1", first)
	}

	second, err := BumpStoreRevision(ctx, sqlDB)
	testutil.FailErr(t, "BumpStoreRevision second", err)
	if second != first+1 {
		t.Fatalf("second revision = %d, want %d", second, first+1)
	}

	reopened, err := Open(filepath.Join(t.TempDir(), "other.db"))
	testutil.FailErr(t, "Open other", err)
	t.Cleanup(func() { _ = reopened.Close() })
	otherFirst, err := BumpStoreRevision(ctx, reopened)
	testutil.FailErr(t, "BumpStoreRevision other", err)
	if otherFirst != 1 {
		t.Fatalf("other db first revision = %d, want 1", otherFirst)
	}
}

func TestOpenBumpStoreRevisionMatchesBootSequence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")

	sqlDB, err := Open(path)
	testutil.FailErr(t, "Open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	if _, err := RunRetention(ctx, sqlDB, DefaultRetention()); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}
	rev, err := BumpStoreRevision(ctx, sqlDB)
	testutil.FailErr(t, "BumpStoreRevision", err)
	if rev != 1 {
		t.Fatalf("boot revision = %d, want 1", rev)
	}
}
