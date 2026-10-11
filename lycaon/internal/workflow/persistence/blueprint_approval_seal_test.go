package persistence_test

import (
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"

	"context"
	"database/sql"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/pkg/api"
)

// A blueprint approval commits its blueprint_approved ledger row in the same
// transaction as the gate it satisfies.
func TestCommitBlueprintApprovalSealsEvent(t *testing.T) {
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "store.db")
	ownerID := testdbseed.OwnerID(t, sqlDB)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	testdbseed.InsertSessionWithRoot(t, sqlDB, "sess-1", testdbseed.DefaultProjectID, projectDir)

	store := workflowpersistence.New(sqlDB)
	store.Transactions.SetAuthzRecorder(authzcontext.SQLRecorder(sqlDB))
	run := &api.WorkflowRun{
		SessionID: "sess-1", ProjectID: testdbseed.DefaultProjectID,
		WorkflowID: "implement", WorkflowVersion: "1",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "plan",
		BlueprintPath: ".paintedwolf/blueprints/auth.md",
	}
	testutil.FailErr(t, "create run", store.State.CreateState(ctx, run, projectDir, map[string]any{}))
	testutil.FailErr(t, "commit approval",
		store.Blueprints.CommitBlueprintApproval(ctx, run, projectDir, map[string]any{}, "digest-1", runstate.ApprovalChannelChat))

	rows, err := authzcontext.NewSQLStore(sqlDB).ListEvents(ctx, "sess-1")
	testutil.FailErr(t, "list events", err)
	if len(rows) != 1 || rows[0].Action != authzcontext.EventActionBlueprintApproved {
		t.Fatalf("events = %+v", rows)
	}
	if rows[0].Outcome != authzcontext.EventOutcomeAllowed || rows[0].ResolvedBy != authzcontext.ResolvedByHuman {
		t.Fatalf("event = %+v", rows[0])
	}
	if rows[0].ResolverPersonID != ownerID {
		t.Fatalf("approval resolver = %q, want host owner %q", rows[0].ResolverPersonID, ownerID)
	}
	var via, approver string
	testutil.FailErr(t, "read approval channel", sqlDB.QueryRowContext(ctx,
		`SELECT approved_via, approved_by_person_id FROM blueprint_approvals WHERE project_id = ? AND path = ?`,
		run.ProjectID, run.BlueprintPath).Scan(&via, &approver))
	if via != runstate.ApprovalChannelChat || approver != ownerID {
		t.Fatalf("approval = via %q by %q", via, approver)
	}
	ok, err := store.Blueprints.BlueprintApprovalMatches(ctx, run.ProjectID, run.BlueprintPath, run.ID, run.Revision-1, "digest-1")
	testutil.FailErr(t, "approval matches", err)
	if !ok {
		t.Fatal("approval row missing after sealed commit")
	}

	var sessionID string
	testutil.FailErr(t, "read session_id", sqlDB.QueryRowContext(ctx, `
		SELECT session_id FROM blueprint_approvals WHERE project_id = ? AND path = ?`,
		run.ProjectID, run.BlueprintPath).Scan(&sessionID))
	if sessionID != "sess-1" {
		t.Fatalf("session_id = %q — the grant must carry its own sealing identity", sessionID)
	}
}

// A re-approval is a new grant: a revoked_at left over from the last one would
// read as a withdrawn approval.
func TestCommitBlueprintApprovalClearsPriorEndOfGrant(t *testing.T) {
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "store.db")
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	testdbseed.InsertSessionWithRoot(t, sqlDB, "sess-1", testdbseed.DefaultProjectID, projectDir)

	store := workflowpersistence.New(sqlDB)
	store.Transactions.SetAuthzRecorder(authzcontext.SQLRecorder(sqlDB))
	run := &api.WorkflowRun{
		SessionID: "sess-1", ProjectID: testdbseed.DefaultProjectID,
		WorkflowID: "implement", WorkflowVersion: "1",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "plan",
		BlueprintPath: ".paintedwolf/blueprints/auth.md",
	}
	testutil.FailErr(t, "create run", store.State.CreateState(ctx, run, projectDir, map[string]any{}))
	testutil.FailErr(t, "commit approval",
		store.Blueprints.CommitBlueprintApproval(ctx, run, projectDir, map[string]any{}, "digest-1", runstate.ApprovalChannelChat))
	_, err := sqlDB.ExecContext(ctx, `
		UPDATE blueprint_approvals SET status = 'revoked', revoked_at = ?, revoked_cause = ?
		WHERE project_id = ? AND path = ?`,
		"2026-01-01T00:00:00Z", db.BlueprintGrantCauseBlueprintDeleted, run.ProjectID, run.BlueprintPath)
	testutil.FailErr(t, "revoke", err)

	testutil.FailErr(t, "re-approve",
		store.Blueprints.CommitBlueprintApproval(ctx, run, projectDir, map[string]any{}, "digest-2", runstate.ApprovalChannelChat))

	var status, cause string
	var revokedAt sql.NullString
	testutil.FailErr(t, "read row", sqlDB.QueryRowContext(ctx, `
		SELECT status, revoked_at, revoked_cause FROM blueprint_approvals WHERE project_id = ? AND path = ?`,
		run.ProjectID, run.BlueprintPath).Scan(&status, &revokedAt, &cause))
	if status != "approved" || revokedAt.Valid || cause != "" {
		t.Fatalf("re-approval kept the prior end-of-grant: status=%q revoked_at=%v cause=%q", status, revokedAt, cause)
	}
}
