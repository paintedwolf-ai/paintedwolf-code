package invocation_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
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
