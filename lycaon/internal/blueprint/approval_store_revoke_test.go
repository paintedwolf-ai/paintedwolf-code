package blueprint

import (
	"context"
	"database/sql"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func seedApprovedBlueprint(t *testing.T, sqlDB db.Handle, path string) (sessionID string) {
	t.Helper()
	ctx := context.Background()
	sessionID = "sess-blueprint"
	testdbseed.InsertSessionWithRoot(t, sqlDB, sessionID, testdbseed.DefaultProjectID, t.TempDir())
	_, err := sqlDB.ExecContext(ctx, `
		INSERT INTO workflow_runs(id, session_id, project_id, workflow_id, workflow_version, status, current_phase, created_at, updated_at)
		VALUES ('run-1', ?, ?, 'implement', '1', 'running', 'start', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		sessionID, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "insert workflow run", err)
	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO blueprint_approvals(project_id, path, content_digest, workflow_run_id, workflow_revision, status, approved_at, approved_via, approved_by_person_id, session_id)
		VALUES (?, ?, 'digest-1', 'run-1', 1, 'approved', '2026-01-01T00:00:00Z', 'chat', (SELECT id FROM people WHERE role = 'owner'), ?)`,
		testdbseed.DefaultProjectID, path, sessionID)
	testutil.FailErr(t, "insert approval", err)
	return sessionID
}

func readApprovalRow(t *testing.T, sqlDB db.Handle, path string) (status, cause string, revokedAt sql.NullString) {
	t.Helper()
	testutil.FailErr(t, "read approval row", sqlDB.QueryRowContext(context.Background(), `
		SELECT status, revoked_cause, revoked_at FROM blueprint_approvals WHERE project_id = ? AND path = ?`,
		testdbseed.DefaultProjectID, path).Scan(&status, &cause, &revokedAt))
	return status, cause, revokedAt
}

func listEvents(t *testing.T, sqlDB db.Handle, sessionID string) []authzcontext.Event {
	t.Helper()
	rows, err := authzcontext.NewSQLStore(sqlDB).ListEvents(context.Background(), sessionID)
	testutil.FailErr(t, "list events", err)
	return rows
}

// Revoke withdraws the grant without rewriting who approved it or when, and
// seals blueprint_revoked into the granting session's chain in the same
// transaction.
func TestRevokeKeepsApprovalIdentityAndSealsEvent(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	const path = ".paintedwolf/blueprints/auth.md"
	sessionID := seedApprovedBlueprint(t, sqlDB, path)

	store := NewApprovalStore(sqlDB, authzcontext.SQLRecorder(sqlDB))
	testutil.FailErr(t, "revoke", store.Revoke(ctx, testdbseed.DefaultProjectID, path))

	var status, approvedAt, approvedVia, cause string
	var revokedAt sql.NullString
	testutil.FailErr(t, "read row", sqlDB.QueryRowContext(ctx, `
		SELECT status, approved_at, approved_via, revoked_at, revoked_cause FROM blueprint_approvals WHERE project_id = ? AND path = ?`,
		testdbseed.DefaultProjectID, path).Scan(&status, &approvedAt, &approvedVia, &revokedAt, &cause))
	if status != "revoked" {
		t.Fatalf("status = %q", status)
	}
	if approvedAt != "2026-01-01T00:00:00Z" || approvedVia != "chat" {
		t.Fatalf("approval identity mutated: approved_at=%q approved_via=%q", approvedAt, approvedVia)
	}
	if !revokedAt.Valid || revokedAt.String == "" {
		t.Fatal("revoked_at not set")
	}
	if cause != db.BlueprintGrantCauseBlueprintDeleted {
		t.Fatalf("revoked_cause = %q", cause)
	}

	rows := listEvents(t, sqlDB, sessionID)
	if len(rows) != 1 || rows[0].Action != authzcontext.EventActionBlueprintRevoked || rows[0].Outcome != authzcontext.EventOutcomeDenied {
		t.Fatalf("events = %+v", rows)
	}

	// Idempotent: a second revoke changes nothing and appends nothing.
	testutil.FailErr(t, "revoke again", store.Revoke(ctx, testdbseed.DefaultProjectID, path))
	if rows = listEvents(t, sqlDB, sessionID); len(rows) != 1 {
		t.Fatalf("second revoke appended events: %+v", rows)
	}
}

// workflow_run_id is ON DELETE SET NULL, so the seal must not depend on it:
// an unlinked run may not turn an end-of-grant into an unledgered write.
func TestRevokeSealsWithUnlinkedWorkflowRun(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	const path = ".paintedwolf/blueprints/auth.md"
	sessionID := seedApprovedBlueprint(t, sqlDB, path)

	_, err := sqlDB.ExecContext(ctx, `DELETE FROM workflow_runs WHERE id = 'run-1'`)
	testutil.FailErr(t, "delete workflow run", err)
	var runID sql.NullString
	testutil.FailErr(t, "read run link", sqlDB.QueryRowContext(ctx, `
		SELECT workflow_run_id FROM blueprint_approvals WHERE project_id = ? AND path = ?`,
		testdbseed.DefaultProjectID, path).Scan(&runID))
	if runID.Valid {
		t.Fatalf("workflow_run_id = %q, want NULL after run delete", runID.String)
	}

	store := NewApprovalStore(sqlDB, authzcontext.SQLRecorder(sqlDB))
	testutil.FailErr(t, "revoke", store.Revoke(ctx, testdbseed.DefaultProjectID, path))

	status, _, _ := readApprovalRow(t, sqlDB, path)
	if status != "revoked" {
		t.Fatalf("status = %q", status)
	}
	rows := listEvents(t, sqlDB, sessionID)
	if len(rows) != 1 || rows[0].Action != authzcontext.EventActionBlueprintRevoked {
		t.Fatalf("unlinked run must still seal; events = %+v", rows)
	}
}

// Superseding is the other way a grant leaves force: recorded too, as a host
// deny rather than a human decision.
func TestSupersedeSealsContentChanged(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	const path = ".paintedwolf/blueprints/auth.md"
	sessionID := seedApprovedBlueprint(t, sqlDB, path)

	store := NewApprovalStore(sqlDB, authzcontext.SQLRecorder(sqlDB))
	testutil.FailErr(t, "supersede", store.Supersede(ctx, testdbseed.DefaultProjectID, path))

	status, cause, revokedAt := readApprovalRow(t, sqlDB, path)
	if status != "superseded" {
		t.Fatalf("status = %q", status)
	}
	if cause != db.BlueprintGrantCauseContentChanged {
		t.Fatalf("revoked_cause = %q", cause)
	}
	if !revokedAt.Valid || revokedAt.String == "" {
		t.Fatal("revoked_at not set")
	}
	rows := listEvents(t, sqlDB, sessionID)
	if len(rows) != 1 || rows[0].Action != authzcontext.EventActionBlueprintSuperseded {
		t.Fatalf("events = %+v", rows)
	}
	if rows[0].Outcome != authzcontext.EventOutcomeDenied || rows[0].ResolvedBy != authzcontext.ResolvedBySystemDeny {
		t.Fatalf("supersede must record as a host deny, not a human decision: %+v", rows[0])
	}

	// A superseded grant is no longer in force, so deleting the blueprint has
	// nothing left to revoke and appends nothing.
	testutil.FailErr(t, "revoke after supersede", store.Revoke(ctx, testdbseed.DefaultProjectID, path))
	if rows = listEvents(t, sqlDB, sessionID); len(rows) != 1 {
		t.Fatalf("revoke of a superseded grant appended events: %+v", rows)
	}
}

// A failed event append aborts the transition: no outcome commits unledgered.
func TestEndOfGrantFailsClosedWhenLedgerAppendFails(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(*ApprovalStore, context.Context, string) error
	}{
		{"revoke", func(s *ApprovalStore, ctx context.Context, path string) error {
			return s.Revoke(ctx, testdbseed.DefaultProjectID, path)
		}},
		{"supersede", func(s *ApprovalStore, ctx context.Context, path string) error {
			return s.Supersede(ctx, testdbseed.DefaultProjectID, path)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sqlDB := testdbfixture.Open(t, "store.db")
			ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
			const path = ".paintedwolf/blueprints/auth.md"
			seedApprovedBlueprint(t, sqlDB, path)

			store := NewApprovalStore(sqlDB, authzcontext.LedgerRecorder{Ledger: &authzcontext.Ledger{Store: authzcontext.FailStore{}}})
			if err := tc.end(store, ctx, path); err == nil {
				t.Fatal("transition must fail when the ledger append fails")
			}
			if status, _, _ := readApprovalRow(t, sqlDB, path); status != "approved" {
				t.Fatalf("committed unledgered; status = %q", status)
			}
		})
	}
}

// Without a recorder there is no chain to seal into, so no grant may end.
func TestEndOfGrantFailsClosedWithoutRecorder(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	const path = ".paintedwolf/blueprints/auth.md"
	seedApprovedBlueprint(t, sqlDB, path)

	store := NewApprovalStore(sqlDB, nil)
	if err := store.Revoke(ctx, testdbseed.DefaultProjectID, path); err == nil {
		t.Fatal("revoke without a recorder must fail closed")
	}
	if err := store.Supersede(ctx, testdbseed.DefaultProjectID, path); err == nil {
		t.Fatal("supersede without a recorder must fail closed")
	}
	if status, _, _ := readApprovalRow(t, sqlDB, path); status != "approved" {
		t.Fatalf("status = %q", status)
	}
}
