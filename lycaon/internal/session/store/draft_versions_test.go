package store

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestStoreDraftVersionsRoundTrip(t *testing.T) {
	store := newSQLTestStore(t, "p1")
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

func TestStoreDraftVersionsCascadeOnSessionDelete(t *testing.T) {
	store := newSQLTestStore(t, "p1")
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{ProjectID: "p1"}, "p1")
	testutil.FailErr(t, "Create", err)
	_, err = store.AppendDraftVersion(ctx, sess.ID, "slot-1", "body", "CODE")
	testutil.FailErr(t, "AppendDraftVersion", err)
	if err := store.Delete(ctx, sess.ID); err != nil {
		testutil.FailErr(t, "store.Delete failed", err)
	}
	n, err := store.CountDraftVersions(ctx, sess.ID, "slot-1")
	testutil.FailErr(t, "count versions after delete", err)
	if n != 0 {
		t.Fatalf("draft versions survived session deletion: %d", n)
	}
}
