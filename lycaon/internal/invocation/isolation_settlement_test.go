package invocation_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSettlementIsolationIgnoresTerminalObservedOutcomes(t *testing.T) {
	// A terminal isolation code observed inside an attempted run stays structured
	// feedback and does not bind the receipt as an isolation outcome without a
	// rejection failure.
	iso := invocation.SettlementIsolation(nil, []string{"SANDBOX_TERMINAL_PATH_REFUSED"})
	if iso != nil {
		t.Fatalf("SettlementIsolation returned %+v, want nil", iso)
	}
}

func TestSettlementIsolationRecordsRetryableObservedOutcomes(t *testing.T) {
	// Retryable outcomes observed during execution bind the receipt for caller retry.
	iso := invocation.SettlementIsolation(nil, []string{"SANDBOX_TRY_READ_PATH"})
	if iso == nil || iso.Code != "SANDBOX_TRY_READ_PATH" || iso.Disposition != "retry" {
		t.Fatalf("SettlementIsolation returned %+v, want SANDBOX_TRY_READ_PATH retry", iso)
	}
}

func TestSettlementIsolationRecordsIsolationRejectionFailure(t *testing.T) {
	failure := &api.InvocationFailure{
		Code:      "SANDBOX_TERMINAL_PATH_REFUSED",
		Class:     api.FailureClassIsolationRejection,
		Retryable: false,
	}
	iso := invocation.SettlementIsolation(failure, nil)
	if iso == nil || iso.Code != "SANDBOX_TERMINAL_PATH_REFUSED" || iso.Disposition != "control_plane" {
		t.Fatalf("SettlementIsolation returned %+v, want SANDBOX_TERMINAL_PATH_REFUSED control_plane", iso)
	}
}

func TestSQLRecorderSettlesCommandWithTerminalObservedFeedback(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract is not declared")
	}
	recorder := invocation.NewSQLRecorder(database)
	receipt, err := recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
		ToolCallID: "call-1", ToolName: "command",
		Args:     map[string]any{"command": "./task test:digest"},
		Contract: contract,
	})
	testutil.FailErr(t, "begin receipt", err)

	iso := invocation.SettlementIsolation(nil, []string{"SANDBOX_TERMINAL_PATH_REFUSED"})
	settled, err := recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status:       api.InvocationStatusCompleted,
		Invoked:      true,
		EvidenceKind: "execution",
		Isolation:    iso,
	})
	testutil.FailErr(t, "settle receipt with terminal feedback", err)
	if settled.Isolation != nil {
		t.Fatalf("settled.Isolation = %+v, want nil", settled.Isolation)
	}
}

// Isolation settlement preserves the selected owner and receipt.
func TestIsolationSettlementStaysOnSelectedOwnerReceipt(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract is not declared")
	}
	recorder := invocation.NewSQLRecorder(database)
	receipt, err := recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
		ToolCallID: "call-1", ToolName: "command",
		Args: map[string]any{"command": []any{"touch", "/outside/file"}}, Contract: contract,
	})
	testutil.FailErr(t, "begin invocation receipt", err)
	settled, err := recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status: api.InvocationStatusRejected, Invoked: false, EvidenceKind: "isolation",
		Isolation: &api.InvocationIsolation{
			Code: isolation.CodeWriteRootDenied, Disposition: string(isolation.DispositionHumanDecision),
		},
		Failure: &api.InvocationFailure{
			Code: isolation.CodeWriteRootDenied, Class: api.FailureClassIsolationRejection,
			Retryable: false, OwnerRef: contract.Owner,
		},
	})
	testutil.FailErr(t, "settle isolation boundary", err)
	if settled.Owner != contract.Owner || settled.Owner == "isolation" {
		t.Fatalf("isolation changed selected owner from %q to %q", contract.Owner, settled.Owner)
	}
	if settled.Invoked {
		t.Fatal("pre-owner isolation stop recorded the subsystem owner as invoked")
	}
	if settled.Failure == nil || settled.Failure.Code != isolation.CodeWriteRootDenied ||
		settled.Failure.Class != api.FailureClassIsolationRejection {
		t.Fatalf("settled isolation failure = %+v", settled.Failure)
	}
	if settled.Isolation == nil || settled.Isolation.Code != isolation.CodeWriteRootDenied ||
		settled.Isolation.Disposition != string(isolation.DispositionHumanDecision) {
		t.Fatalf("settled isolation outcome = %+v", settled.Isolation)
	}

	receipt, err = recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
		ToolCallID: "call-2", ToolName: "command",
		Args: map[string]any{"command": []any{"touch", "/outside/file"}}, Contract: contract,
	})
	testutil.FailErr(t, "begin attempted-effect receipt", err)
	settled, err = recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status: api.InvocationStatusCompleted, Invoked: true, EvidenceKind: string(contract.Evidence()),
		Isolation: &api.InvocationIsolation{
			Code: isolation.CodeTryWriteRoot, Disposition: string(isolation.DispositionRetry),
		},
	})
	testutil.FailErr(t, "settle low-level isolation observation", err)
	if settled.Owner != contract.Owner || !settled.Invoked || settled.Failure != nil ||
		settled.Isolation == nil || settled.Isolation.Code != isolation.CodeTryWriteRoot {
		t.Fatalf("low-level isolation receipt = %+v", settled)
	}
}

func TestInvocationReceiptRejectsUntypedIsolationSettlement(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract is not declared")
	}
	recorder := invocation.NewSQLRecorder(database)
	receipt, err := recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
		ToolCallID: "call-1", ToolName: "command",
		Args: map[string]any{"command": []any{"true"}}, Contract: contract,
	})
	testutil.FailErr(t, "begin invocation receipt", err)
	_, err = recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status: api.InvocationStatusRejected, EvidenceKind: "rejection",
		Failure: &api.InvocationFailure{
			Code: isolation.CodeTryWriteRoot, Class: api.FailureClassHostRejection, Retryable: true,
		},
	})
	assertIsolationSettlementRefused(t, recorder, receipt.ID, err, invocation.ErrIsolationClassMismatch)
}

func TestInvocationReceiptRejectsMalformedTerminalIsolation(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract is not declared")
	}
	recorder := invocation.NewSQLRecorder(database)
	receipt, err := recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
		ToolCallID: "call-1", ToolName: "command",
		Args: map[string]any{"command": []any{"true"}}, Contract: contract,
	})
	testutil.FailErr(t, "begin invocation receipt", err)
	_, err = recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status: api.InvocationStatusCompleted, Invoked: true, EvidenceKind: string(contract.Evidence()),
		Isolation: &api.InvocationIsolation{
			Code: isolation.CodeControlPlaneDenied, Disposition: string(isolation.DispositionControlPlane),
		},
	})
	assertIsolationSettlementRefused(t, recorder, receipt.ID, err, invocation.ErrIsolationRejectionMissing)

	receipt, err = recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
		ToolCallID: "call-2", ToolName: "command",
		Args: map[string]any{"command": []any{"true"}}, Contract: contract,
	})
	testutil.FailErr(t, "begin errored terminal receipt", err)
	_, err = recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status: api.InvocationStatusError, Invoked: true, EvidenceKind: "error",
		Isolation: &api.InvocationIsolation{
			Code: isolation.CodeControlPlaneDenied, Disposition: string(isolation.DispositionControlPlane),
		},
		Failure: &api.InvocationFailure{
			Code: isolation.CodeControlPlaneDenied, Class: api.FailureClassIsolationRejection,
			Retryable: false, OwnerRef: contract.Owner,
		},
	})
	assertIsolationSettlementRefused(t, recorder, receipt.ID, err, invocation.ErrIsolationTerminalNotRejected)
}

// File-change and process reviews run at the owner's effect seam, after the
// owner started. Their terminal refusals settle as rejections that keep the
// owner's invoked fact instead of failing the turn.
func TestInvocationReceiptSettlesOwnerSeamTerminalRejection(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	recorder := invocation.NewSQLRecorder(database)
	for _, tc := range []struct {
		tool string
		code string
	}{
		{"write", isolation.CodeControlPlaneDenied},
		{"process_signal", isolation.CodeExecutionCapabilityDenied},
	} {
		contract, ok := toolcontract.Lookup(tc.tool)
		if !ok {
			t.Fatalf("%s contract is not declared", tc.tool)
		}
		outcome, ok := isolation.Lookup(tc.code)
		if !ok || outcome.Retryable() {
			t.Fatalf("%s is not a terminal isolation outcome", tc.code)
		}
		receipt, err := recorder.Begin(t.Context(), invocation.Start{
			ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
			ToolCallID: "call-" + tc.tool, ToolName: tc.tool,
			Args: map[string]any{"path": "notes.md"}, Contract: contract,
		})
		testutil.FailErr(t, "begin "+tc.tool+" receipt", err)
		settled, err := recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
			Status: api.InvocationStatusRejected, Invoked: true, EvidenceKind: "rejection",
			Isolation: &api.InvocationIsolation{Code: outcome.Code, Disposition: string(outcome.Disposition)},
			Failure: &api.InvocationFailure{
				Code: outcome.Code, Class: api.FailureClassIsolationRejection,
				Retryable: false, OwnerRef: contract.Owner,
			},
		})
		testutil.FailErr(t, "settle "+tc.tool+" owner-seam rejection", err)
		if settled.Status != api.InvocationStatusRejected || !settled.Invoked ||
			settled.Isolation == nil || settled.Isolation.Code != outcome.Code ||
			settled.Failure == nil || settled.Failure.Code != outcome.Code {
			t.Fatalf("%s owner-seam receipt = %+v", tc.tool, settled)
		}
	}
}

// assertIsolationSettlementRefused checks a refused settlement by its typed
// rule and by the ledger: the receipt closes as a host fault, never with the
// isolation outcome it was refused.
func assertIsolationSettlementRefused(t *testing.T, recorder *invocation.SQLRecorder, receiptID string, err, rule error) {
	t.Helper()
	var refused *invocation.SettlementRefusedError
	if !errors.As(err, &refused) || !errors.Is(err, rule) || refused.ReceiptID != receiptID {
		t.Fatalf("settlement error = %v, want %v refusing %s", err, rule, receiptID)
	}
	listed, err := recorder.ListSession(t.Context(), "session-1")
	testutil.FailErr(t, "list receipts", err)
	var settled *api.InvocationReceipt
	for i := range listed {
		if listed[i].ID == receiptID {
			settled = &listed[i]
		}
	}
	if settled == nil || settled.Status != api.InvocationStatusError || settled.Isolation != nil || settled.Failure == nil ||
		settled.Failure.Code != invocation.SettlementRefusedCode || settled.Failure.Class != api.FailureClassHostFault {
		t.Fatalf("refused receipt = %+v, want a host fault without isolation", settled)
	}
}
