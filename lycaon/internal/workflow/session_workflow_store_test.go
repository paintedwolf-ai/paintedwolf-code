package workflow

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
	store := NewMemorySessionWorkflowStore()
	ctx := context.Background()
	if err := store.Upsert(ctx, "sess-1", []byte(testSessionManifest), ComposeActorCoordinator, nil); err != nil {
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
	if err := store.Upsert(ctx, "sess-1", []byte(updated), ComposeActorCoordinator, nil); err != nil {
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
	store := NewMemorySessionWorkflowStore()
	ctx := context.Background()
	if err := store.Upsert(ctx, "sess-1", []byte(testSessionManifest), ComposeActorCoordinator, nil); err != nil {
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
	store := NewMemorySessionWorkflowStore()
	err := store.Upsert(context.Background(), "sess-1", []byte("not: yaml: {"), ComposeActorCoordinator, nil)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestSessionWorkflowStoreRejectsUnknownActor(t *testing.T) {
	store := NewMemorySessionWorkflowStore()
	err := store.Upsert(context.Background(), "sess-1", []byte(testSessionManifest), ComposeActor("forged"), nil)
	if err == nil {
		t.Fatal("expected invalid compose actor error")
	}
}

func TestSessionWorkflowSQLStoreCascadeDelete(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "session_wf.db")

	ctx := context.Background()
	testdbseed.InsertSession(t, sqlDB, "sess-cascade", testdbseed.DefaultProjectID)

	store := NewSessionWorkflowSQLStore(sqlDB)
	if err := store.Upsert(ctx, "sess-cascade", []byte(testSessionManifest), ComposeActorCoordinator, nil); err != nil {
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

	store := NewSessionWorkflowSQLStore(sqlDB)
	_, err := store.Get(context.Background(), "missing", "x", "1.0.0")
	if !errors.Is(err, ErrSessionWorkflowNotFound) {
		t.Fatalf("err = %v want ErrSessionWorkflowNotFound", err)
	}
}
