package db

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func seedGrant(t *testing.T, sqlDB Handle, path, sessionID, status string) {
	t.Helper()
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT INTO blueprint_approvals(
			project_id, path, content_digest, workflow_revision, status, approved_at, approved_via, approved_by_person_id, session_id
		) VALUES (?, ?, 'digest', 0, ?, '2026-01-01T00:00:00Z', 'chat', (SELECT id FROM people WHERE role = 'owner'), ?)`,
		testdbseed.DefaultProjectID, path, status, sessionID)
	testutil.FailErr(t, "insert blueprint approval", err)
}

func grantRow(t *testing.T, sqlDB DBTX, path string) (status, cause string) {
	t.Helper()
	testutil.FailErr(t, "read blueprint approval", sqlDB.QueryRowContext(t.Context(), `
		SELECT status, revoked_cause FROM blueprint_approvals WHERE project_id = ? AND path = ?`,
		testdbseed.DefaultProjectID, path).Scan(&status, &cause))
	return status, cause
}

// Session deletion revokes the grants it authorized.
func TestRevokeBlueprintApprovalsForSession(t *testing.T) {
	ctx := context.Background()
	sqlDB := openTestDB(t)
	insertTestSession(t, sqlDB, "granting-sess")
	insertTestSession(t, sqlDB, "other-sess")
	seedGrant(t, sqlDB, ".paintedwolf/blueprints/in-force.md", "granting-sess", "approved")
	seedGrant(t, sqlDB, ".paintedwolf/blueprints/already-superseded.md", "granting-sess", "superseded")
	seedGrant(t, sqlDB, ".paintedwolf/blueprints/other.md", "other-sess", "approved")

	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin", err)
	n, err := RevokeBlueprintApprovalsForSession(ctx, tx, "granting-sess", time.Now().UTC())
	testutil.FailErr(t, "RevokeBlueprintApprovalsForSession", err)
	testutil.FailErr(t, "commit", tx.Commit())
	if n != 1 {
		t.Fatalf("revoked = %d want 1 (only the in-force grant)", n)
	}

	if status, cause := grantRow(t, sqlDB, ".paintedwolf/blueprints/in-force.md"); status != "revoked" || cause != BlueprintGrantCauseSessionDeleted {
		t.Fatalf("in-force grant = %q/%q", status, cause)
	}
	// Superseded grants retain their cause.
	if status, cause := grantRow(t, sqlDB, ".paintedwolf/blueprints/already-superseded.md"); status != "superseded" || cause != "" {
		t.Fatalf("superseded grant rewritten: %q/%q", status, cause)
	}
	if status, _ := grantRow(t, sqlDB, ".paintedwolf/blueprints/other.md"); status != "approved" {
		t.Fatalf("other session's grant = %q, want untouched", status)
	}
}

// Retention revokes grants whose session is absent.
func TestRetentionRevokesOrphanBlueprintGrants(t *testing.T) {
	ctx := context.Background()
	sqlDB := openTestDB(t)
	insertTestSession(t, sqlDB, "live-sess")
	seedGrant(t, sqlDB, ".paintedwolf/blueprints/live.md", "live-sess", "approved")
	seedGrant(t, sqlDB, ".paintedwolf/blueprints/orphan.md", "vanished-sess", "approved")

	cfg := DefaultRetention()
	cfg.IncrementalVacuumMinFreelistPages = 9999
	rep, err := RunRetention(ctx, sqlDB, cfg)
	testutil.FailErr(t, "RunRetention", err)
	if rep.OrphanBlueprintGrants != 1 {
		t.Fatalf("orphan grants revoked = %d want 1", rep.OrphanBlueprintGrants)
	}
	if status, cause := grantRow(t, sqlDB, ".paintedwolf/blueprints/orphan.md"); status != "revoked" || cause != BlueprintGrantCauseSessionDeleted {
		t.Fatalf("orphan grant = %q/%q", status, cause)
	}
	if status, _ := grantRow(t, sqlDB, ".paintedwolf/blueprints/live.md"); status != "approved" {
		t.Fatalf("live grant = %q, want untouched", status)
	}
}
