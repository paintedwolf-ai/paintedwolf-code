package invocation_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

// A settlement the ledger refuses closes the receipt as a host fault instead
// of leaving it running, and keeps the owner's invoked fact.
func TestSQLRecorderSettlesRefusedOutcomeAsHostFault(t *testing.T) {
	for _, invoked := range []bool{false, true} {
		database := testdbfixture.Open(t, "store.db")
		testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
		contract, ok := toolcontract.Lookup("write")
		if !ok {
			t.Fatal("write contract is not declared")
		}
		recorder := invocation.NewSQLRecorder(database)
		receipt, err := recorder.Begin(t.Context(), invocation.Start{
			ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
			ToolCallID: "call-1", ToolName: "write",
			Args: map[string]any{"path": "notes.md"}, Contract: contract,
		})
		testutil.FailErr(t, "begin receipt", err)

		// A terminal isolation outcome on an errored receipt is not a settlement the ledger records.
		_, err = recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
			Status: api.InvocationStatusError, Invoked: invoked, EvidenceKind: "error",
			Isolation: &api.InvocationIsolation{
				Code: isolation.CodeControlPlaneDenied, Disposition: string(isolation.DispositionControlPlane),
			},
			Failure: &api.InvocationFailure{
				Code: isolation.CodeControlPlaneDenied, Class: api.FailureClassIsolationRejection,
			},
		})
		var refused *invocation.SettlementRefusedError
		if !errors.As(err, &refused) || refused.ReceiptID != receipt.ID ||
			!strings.Contains(err.Error(), "requires a rejected receipt") {
			t.Fatalf("refused settlement error = %v", err)
		}
		fault := refused.Receipt
		if fault == nil || fault.Status != api.InvocationStatusError || fault.Invoked != invoked ||
			fault.Evidence.Kind != invocation.EvidenceKindHostFault || fault.Isolation != nil ||
			fault.Failure == nil || fault.Failure.Code != invocation.SettlementRefusedCode ||
			fault.Failure.Class != api.FailureClassHostFault || fault.Failure.Retryable {
			t.Fatalf("host-fault receipt (invoked=%v) = %+v", invoked, fault)
		}
		listed, err := recorder.ListSession(t.Context(), "session-1")
		testutil.FailErr(t, "list receipts", err)
		if len(listed) != 1 || listed[0].Status != api.InvocationStatusError || listed[0].SettledAt == nil {
			t.Fatalf("ledger after refusal = %+v", listed)
		}
		if interrupted, err := recorder.InterruptRunning(t.Context()); err != nil || interrupted != 0 {
			t.Fatalf("refused receipt was still running: interrupted=%d err=%v", interrupted, err)
		}
	}
}

// A second settlement is refused without overwriting the recorded outcome.
func TestSQLRecorderRefusesSecondSettlementWithoutOverwriting(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	contract, ok := toolcontract.Lookup("read")
	if !ok {
		t.Fatal("read contract is not declared")
	}
	recorder := invocation.NewSQLRecorder(database)
	receipt, err := recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
		ToolCallID: "call-1", ToolName: "read",
		Args: map[string]any{"path": "notes.md"}, Contract: contract,
	})
	testutil.FailErr(t, "begin receipt", err)
	_, err = recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status: api.InvocationStatusCompleted, Invoked: true, EvidenceKind: string(contract.Evidence()),
	})
	testutil.FailErr(t, "first settlement", err)

	_, err = recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status: api.InvocationStatusError, Invoked: true, EvidenceKind: "error",
		Failure: &api.InvocationFailure{Code: "TOOL_OWNER_FAILED", Class: api.FailureClassOwnerError},
	})
	var refused *invocation.SettlementRefusedError
	if !errors.As(err, &refused) || refused.Receipt != nil {
		t.Fatalf("second settlement error = %v", err)
	}
	listed, err := recorder.ListSession(t.Context(), "session-1")
	testutil.FailErr(t, "list receipts", err)
	if len(listed) != 1 || listed[0].Status != api.InvocationStatusCompleted {
		t.Fatalf("second settlement overwrote the first: %+v", listed)
	}
}
