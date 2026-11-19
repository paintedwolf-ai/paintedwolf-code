package drafts

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

const testSessionManifest = `id: hotfix-session
version: 1.0.0
phases:
  - id: only
    activity_label: Test phase
    complete_when: plan_stub_valid
`

func TestSessionWorkflowStoreUpsertAndList(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	if err := store.Upsert(ctx, "sess-1", []byte(testSessionManifest), Coordinator, nil); err != nil {
		testutil.FailErr(t, "store.Upsert failed", err)
	}
	rows, err := store.ListBySession(ctx, "sess-1")
	testutil.FailErr(t, "store.ListBySession failed", err)
	if len(rows) != 1 {
		t.Fatalf("rows = %d want 1", len(rows))
	}
	if rows[0].WorkflowID != "hotfix-session" {
		t.Fatalf("workflow_id = %q", rows[0].WorkflowID)
	}

	updated := `id: hotfix-session
version: 1.0.0
name: updated
phases:
  - id: only
    activity_label: Test phase
    complete_when: plan_stub_valid
`
	if err := store.Upsert(ctx, "sess-1", []byte(updated), Coordinator, nil); err != nil {
		testutil.FailErr(t, "store.Upsert failed", err)
	}
	got, err := store.Get(ctx, "sess-1", "hotfix-session", "1.0.0")
	testutil.FailErr(t, "store.Get failed", err)
	if got.CreatedBy != "coordinator" {
		t.Fatalf("created_by = %q", got.CreatedBy)
	}
	if got.ManifestYAML != updated {
		t.Fatalf("manifest not replaced")
	}
}

func TestSessionWorkflowStoreDelete(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	if err := store.Upsert(ctx, "sess-1", []byte(testSessionManifest), Coordinator, nil); err != nil {
		testutil.FailErr(t, "store.Upsert failed", err)
	}
	if err := store.Delete(ctx, "sess-1", "hotfix-session", "1.0.0"); err != nil {
		testutil.FailErr(t, "store.Delete failed", err)
	}
	rows, err := store.ListBySession(ctx, "sess-1")
	testutil.FailErr(t, "store.ListBySession failed", err)
	if len(rows) != 0 {
		t.Fatalf("rows = %d want 0", len(rows))
	}
}

func TestSessionWorkflowStoreRejectInvalidYAML(t *testing.T) {
	store := NewMemory()
	err := store.Upsert(context.Background(), "sess-1", []byte("not: yaml: {"), Coordinator, nil)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestSessionWorkflowStoreRejectsUnknownActor(t *testing.T) {
	store := NewMemory()
	err := store.Upsert(context.Background(), "sess-1", []byte(testSessionManifest), Actor("forged"), nil)
	if err == nil {
		t.Fatal("expected invalid compose actor error")
	}
}

func TestSessionWorkflowSQLStoreCascadeDelete(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "session_wf.db")

	ctx := context.Background()
	testdbseed.InsertSession(t, sqlDB, "sess-cascade", testdbseed.DefaultProjectID)

	store := NewSQL(sqlDB)
	if err := store.Upsert(ctx, "sess-cascade", []byte(testSessionManifest), Coordinator, nil); err != nil {
		testutil.FailErr(t, "store.Upsert failed", err)
	}
	res, err := sqlDB.ExecContext(ctx, `DELETE FROM sessions WHERE id = 'sess-cascade'`)
	testutil.FailErr(t, "sqlDB.ExecContext failed", err)
	n, _ := res.RowsAffected()
	if n != 1 {
		t.Fatalf("deleted sessions = %d", n)
	}
	rows, err := store.ListBySession(ctx, "sess-cascade")
	testutil.FailErr(t, "store.ListBySession failed", err)
	if len(rows) != 0 {
		t.Fatalf("orphan session_workflows rows = %d", len(rows))
	}
}

func TestSessionWorkflowSQLStoreGetNotFound(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "session_wf.db")

	store := NewSQL(sqlDB)
	_, err := store.Get(context.Background(), "missing", "x", "1.0.0")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v want ErrNotFound", err)
	}
}

func TestPublicationKeepsSupersedingDraft(t *testing.T) {
	database := testdbfixture.Open(t, "published.db")
	ctx := t.Context()
	testdbseed.InsertSession(t, database, "publish", testdbseed.DefaultProjectID)
	for _, store := range []Store{NewSQL(database), NewMemory()} {
		put := func(body string) *Record {
			t.Helper()
			testutil.FailErr(t, "compose draft", store.Upsert(ctx, "publish", []byte(testSessionManifest), Coordinator, &Composition{AuthoredYAML: body}))
			r, err := store.Get(ctx, "publish", "hotfix-session", "1.0.0")
			testutil.FailErr(t, "read draft", err)
			return r
		}
		original := put("original")
		next := put("revised")
		testutil.FailErr(t, "consume original", store.DeletePublished(ctx, *original))
		got, err := store.Get(ctx, "publish", "hotfix-session", "1.0.0")
		testutil.FailErr(t, "recover newer draft", err)
		if got.AuthoredYAML != next.AuthoredYAML {
			t.Fatalf("draft source = %q", got.AuthoredYAML)
		}
		testutil.FailErr(t, "consume current draft", store.DeletePublished(ctx, *next))
		testutil.FailErr(t, "retry consumption", store.DeletePublished(ctx, *next))
		if _, err := store.Get(ctx, "publish", "hotfix-session", "1.0.0"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("consumed draft = %v", err)
		}
	}
}
