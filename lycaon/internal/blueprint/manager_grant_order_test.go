package blueprint

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// approvedManager returns a manager whose blueprint is approved for its current
// bytes, sealing into the recorder the factory builds.
func approvedManager(t *testing.T, recorderFor func(db.Handle) authzledger.TransactionalRecorder) (*Manager, db.Handle, *api.Blueprint) {
	t.Helper()
	ctx := context.Background()
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSessionWithRoot(t, sqlDB, "sess-grant", testdbseed.DefaultProjectID, projectDir)

	mgr := NewManager(NewFileStoreForTest(projectDir))
	mgr.Approvals = NewApprovalStore(sqlDB, recorderFor(sqlDB))
	bp, err := mgr.Create(ctx, testdbseed.DefaultProjectID, "Grant", ConventionPath("granted.md"), "plan", "")
	testutil.FailErr(t, "Create", err)
	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO blueprint_approvals(project_id, path, content_digest, workflow_revision, status, approved_at, approved_via, approved_by_person_id, session_id)
		VALUES (?, ?, ?, 0, 'approved', '2026-01-01T00:00:00Z', 'chat', (SELECT id FROM people WHERE role = 'owner'), 'sess-grant')`,
		testdbseed.DefaultProjectID, bp.Path, ContentDigest(bp.Content))
	testutil.FailErr(t, "insert approval", err)

	approved, err := mgr.IsApproved(ctx, testdbseed.DefaultProjectID, bp.Path)
	testutil.FailErr(t, "IsApproved", err)
	if !approved {
		t.Fatal("fixture blueprint must start approved")
	}
	return mgr, sqlDB, bp
}

// The grant ends before the bytes move: an unsealable transition leaves the file
// alone, so no approved row is left bound to re-creatable bytes.
func TestUpdateAndDeleteEndTheGrantBeforeTouchingBytes(t *testing.T) {
	ctx := context.Background()
	failing := func(db.Handle) authzledger.TransactionalRecorder {
		return authzcontext.LedgerRecorder{Ledger: &authzcontext.Ledger{Store: authzcontext.FailStore{}}}
	}

	t.Run("update", func(t *testing.T) {
		mgr, sqlDB, bp := approvedManager(t, failing)
		body := "# Rewritten\n"
		if _, err := mgr.Update(ctx, testdbseed.DefaultProjectID, bp.Path, &body, nil); err == nil {
			t.Fatal("update must fail when the supersede cannot be sealed")
		}
		after, err := mgr.Store.Get(ctx, testdbseed.DefaultProjectID, bp.Path)
		testutil.FailErr(t, "Get", err)
		if after.Content != bp.Content {
			t.Fatalf("content changed despite unsealed supersede: %q", after.Content)
		}
		assertApprovalStatus(t, sqlDB, bp.Path, "approved")
	})

	t.Run("delete", func(t *testing.T) {
		mgr, sqlDB, bp := approvedManager(t, failing)
		if err := mgr.Delete(ctx, testdbseed.DefaultProjectID, bp.Path); err == nil {
			t.Fatal("delete must fail when the revoke cannot be sealed")
		}
		if _, err := mgr.Store.Get(ctx, testdbseed.DefaultProjectID, bp.Path); err != nil {
			t.Fatalf("blueprint unlinked despite unsealed revoke: %v", err)
		}
		assertApprovalStatus(t, sqlDB, bp.Path, "approved")
	})
}

// The sealed path: both transitions end the grant, record it, then move the bytes.
func TestUpdateSupersedesAndDeleteRevokes(t *testing.T) {
	ctx := context.Background()
	t.Run("update", func(t *testing.T) {
		mgr, sqlDB, bp := approvedManager(t, sealingRecorder)
		body := "# Rewritten\n"
		updated, err := mgr.Update(ctx, testdbseed.DefaultProjectID, bp.Path, &body, nil)
		testutil.FailErr(t, "Update", err)
		if updated.Status != api.BlueprintStatusDraft {
			t.Fatalf("status = %q", updated.Status)
		}
		assertApprovalStatus(t, sqlDB, bp.Path, "superseded")
		assertSealed(t, sqlDB, authzcontext.EventActionBlueprintSuperseded)
	})

	t.Run("delete", func(t *testing.T) {
		mgr, sqlDB, bp := approvedManager(t, sealingRecorder)
		ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
		testutil.FailErr(t, "Delete", mgr.Delete(ctx, testdbseed.DefaultProjectID, bp.Path))
		if _, err := mgr.Store.Get(ctx, testdbseed.DefaultProjectID, bp.Path); err == nil {
			t.Fatal("blueprint must be unlinked after a sealed revoke")
		}
		assertApprovalStatus(t, sqlDB, bp.Path, "revoked")
		assertSealed(t, sqlDB, authzcontext.EventActionBlueprintRevoked)
	})
}

func sealingRecorder(sqlDB db.Handle) authzledger.TransactionalRecorder {
	return authzcontext.SQLRecorder(sqlDB)
}

func assertApprovalStatus(t *testing.T, sqlDB db.Handle, path, want string) {
	t.Helper()
	var got string
	testutil.FailErr(t, "read approval", sqlDB.QueryRowContext(context.Background(), `
		SELECT status FROM blueprint_approvals WHERE project_id = ? AND path = ?`,
		testdbseed.DefaultProjectID, path).Scan(&got))
	if got != want {
		t.Fatalf("approval status = %q want %q", got, want)
	}
}

func assertSealed(t *testing.T, sqlDB db.Handle, want authzcontext.EventAction) {
	t.Helper()
	rows, err := authzcontext.NewSQLStore(sqlDB).ListEvents(context.Background(), "sess-grant")
	testutil.FailErr(t, "list events", err)
	if len(rows) != 1 || rows[0].Action != want {
		t.Fatalf("events = %+v want one %q", rows, want)
	}
}
