package store

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestMemoryStoreDraftVersionsRoundTrip(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{ProjectID: "p1"}, "p1")
	testutil.FailErr(t, "Create", err)

	count, err := store.AppendDraftVersion(ctx, sess.ID, "slot-1", "body v0", "CODE_A")
	testutil.FailErr(t, "AppendDraftVersion", err)
	if count != 1 {
		t.Fatalf("count = %d want 1", count)
	}
	count, err = store.AppendDraftVersion(ctx, sess.ID, "slot-1", "body v1", "CODE_B")
	testutil.FailErr(t, "AppendDraftVersion second", err)
	if count != 2 {
		t.Fatalf("count = %d want 2", count)
	}
	got, err := store.ListDraftVersions(ctx, sess.ID, "slot-1")
	testutil.FailErr(t, "ListDraftVersions", err)
	if len(got) != 2 || got[0].Body != "body v0" || got[1].OutcomeCode != "CODE_B" {
		t.Fatalf("versions = %+v", got)
	}
	n, err := store.CountDraftVersions(ctx, sess.ID, "slot-1")
	testutil.FailErr(t, "CountDraftVersions", err)
	if n != 2 {
		t.Fatalf("n = %d want 2", n)
	}
}

func TestMemoryStoreDraftVersionsCascadeOnSessionDelete(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{ProjectID: "p1"}, "p1")
	testutil.FailErr(t, "Create", err)
	_, err = store.AppendDraftVersion(ctx, sess.ID, "slot-1", "body", "CODE")
	testutil.FailErr(t, "AppendDraftVersion", err)
	if err := store.Delete(ctx, sess.ID); err != nil {
		testutil.FailErr(t, "store.Delete failed", err)
	}
	n, err := store.CountDraftVersions(ctx, sess.ID, "slot-1")
	if err == nil {
		t.Fatalf("expected error after session delete, count=%d", n)
	}
}

func TestSQLStoreDraftVersionsRoundTrip(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "draft-versions.db")

	store := NewSQL(sqlDB)
	ctx := context.Background()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	if _, err := store.AppendDraftVersion(ctx, sess.ID, "slot-1", "v0", "A"); err != nil {
		testutil.FailErr(t, "AppendDraftVersion", err)
	}
	if _, err := store.AppendDraftVersion(ctx, sess.ID, "slot-1", "v1", "B"); err != nil {
		testutil.FailErr(t, "AppendDraftVersion", err)
	}
	got, err := store.ListDraftVersions(ctx, sess.ID, "slot-1")
	testutil.FailErr(t, "ListDraftVersions", err)
	if len(got) != 2 {
		t.Fatalf("len = %d want 2", len(got))
	}
	if err := store.Delete(ctx, sess.ID); err != nil {
		testutil.FailErr(t, "store.Delete failed", err)
	}
	n, err := store.CountDraftVersions(ctx, sess.ID, "slot-1")
	testutil.FailErr(t, "CountDraftVersions after delete", err)
	if n != 0 {
		t.Fatalf("count after delete = %d want 0", n)
	}
}
