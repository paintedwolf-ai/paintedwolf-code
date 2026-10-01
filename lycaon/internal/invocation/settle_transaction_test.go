package invocation_test

import (
	"context"
	"database/sql"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

type blindReadHandle struct {
	db.Handle
}

func (h blindReadHandle) QueryRowContext(ctx context.Context, _ string, _ ...any) *sql.Row {
	return h.Handle.QueryRowContext(ctx, "SELECT 1 WHERE 0")
}

func TestSQLRecorderSettlementDoesNotDependOnReadPoolVisibility(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	contract, ok := toolcontract.Lookup("task")
	if !ok {
		t.Fatal("task contract is not declared")
	}
	recorder := invocation.NewSQLRecorder(blindReadHandle{Handle: database})
	receipt, err := recorder.Begin(t.Context(), invocation.Start{
		ProjectID:  testdbseed.DefaultProjectID,
		SessionID:  "session-1",
		ToolCallID: "call-1",
		ToolName:   "task",
		Args:       map[string]any{"agent_type": "implementer"},
		Contract:   contract,
	})
	testutil.FailErr(t, "begin receipt", err)

	settled, err := recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status:       api.InvocationStatusCompleted,
		Invoked:      true,
		EvidenceKind: string(contract.Evidence()),
		OwnerRef:     "job-1",
	})
	testutil.FailErr(t, "settle receipt", err)
	if settled.Status != api.InvocationStatusCompleted || settled.Evidence.OwnerRef != "job-1" {
		t.Fatalf("settled receipt = %+v", settled)
	}
}
