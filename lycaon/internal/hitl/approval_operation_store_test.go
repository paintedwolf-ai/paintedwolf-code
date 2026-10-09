package hitl

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

type recoveryApprovalInstaller struct {
	rolledBack []string
}

func (*recoveryApprovalInstaller) InstallApprovalOption(context.Context, string, ApprovalOption) (func(), error) {
	return func() {}, nil
}

func (r *recoveryApprovalInstaller) RollbackApprovalOption(_ context.Context, checkpointID string, _ ApprovalOption) error {
	r.rolledBack = append(r.rolledBack, checkpointID)
	return nil
}

func TestRecoverApprovalOperationsRollsBackPreparedAuthority(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSessionWithRoot(t, sqlDB, "session-1", testdbseed.DefaultProjectID, t.TempDir())
	store := NewSQLStore(sqlDB)
	checkpoint := StoredCheckpoint{
		ID: "checkpoint-1", SessionID: "session-1", ProjectID: testdbseed.DefaultProjectID,
		Kind: api.CheckpointKindToolApproval, Status: DecisionStatusPending, Type: DecisionTypeApprove,
		Title: "Approve", Payload: map[string]any{}, CreatedAt: time.Now().UTC(),
	}
	testutil.FailErr(t, "insert checkpoint", store.Insert(t.Context(), checkpoint))
	option := CurrentActionOption()
	testutil.FailErr(t, "prepare operation", store.prepareApprovalOperation(t.Context(), checkpoint.ID, checkpoint.SessionID, option))
	installer := &recoveryApprovalInstaller{}
	manager := NewCheckpoints(store, nil, &fakeAuthzRecorder{})
	manager.Authority.SetApprovalAuthorityInstaller(installer)
	testutil.FailErr(t, "recover operations", manager.Authority.RecoverApprovalOperations(t.Context()))
	if len(installer.rolledBack) != 1 || installer.rolledBack[0] != checkpoint.ID {
		t.Fatalf("rolled back = %v", installer.rolledBack)
	}
	var status string
	testutil.FailErr(t, "read operation", sqlDB.QueryRowContext(t.Context(), `SELECT status FROM approval_operations WHERE checkpoint_id = ?`, checkpoint.ID).Scan(&status))
	if status != "rolled_back" {
		t.Fatalf("status = %q", status)
	}
	testutil.FailErr(t, "prepare retry", store.prepareApprovalOperation(t.Context(), checkpoint.ID, checkpoint.SessionID, option))
	testutil.FailErr(t, "read retried operation", sqlDB.QueryRowContext(t.Context(), `SELECT status FROM approval_operations WHERE checkpoint_id = ?`, checkpoint.ID).Scan(&status))
	if status != "prepared" {
		t.Fatalf("retried status = %q", status)
	}
	stored, err := store.Runs.Get(t.Context(), checkpoint.ID)
	testutil.FailErr(t, "get checkpoint", err)
	if stored.Status != DecisionStatusPending {
		t.Fatalf("checkpoint status = %q", stored.Status)
	}
}
