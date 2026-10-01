package invocation_test

import (
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestSQLRecorderRejectsNilDatabase(t *testing.T) {
	recorder := invocation.NewSQLRecorder(nil)
	if _, err := recorder.Begin(t.Context(), invocation.Start{}); err == nil {
		t.Fatal("nil database recorder accepted a receipt")
	}
}

func TestSQLRecorderSettlesContractReceipt(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	contract, ok := toolcontract.Lookup("task")
	if !ok {
		t.Fatal("task contract is not declared")
	}
	recorder := invocation.NewSQLRecorder(database)
	receipt, err := recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
		ToolCallID: "call-1", ToolName: "task",
		Args: map[string]any{
			"agent_type": "implementer",
			"brief": map[string]any{
				"goal":      "build",
				"done_when": []string{"Return results."},
			},
		}, Contract: contract,
	})
	testutil.FailErr(t, "begin receipt", err)
	if receipt.Status != api.InvocationStatusRunning || receipt.Owner != "workers" {
		t.Fatalf("running receipt = %+v", receipt)
	}
	running, err := recorder.ListSession(t.Context(), "session-1")
	testutil.FailErr(t, "list running receipt", err)
	if len(running) != 1 || running[0].Evidence.Kind != "pending" {
		t.Fatalf("running receipts = %+v", running)
	}
	settled, err := recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status: api.InvocationStatusCompleted, Invoked: true,
		EvidenceKind: string(contract.Evidence()), EvidenceRef: "message-1", OwnerRef: "job-1",
	})
	testutil.FailErr(t, "settle receipt", err)
	if settled.Status != api.InvocationStatusCompleted || settled.Evidence.OwnerRef != "job-1" || !settled.Invoked {
		t.Fatalf("settled receipt = %+v", settled)
	}
	if _, err := recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status: api.InvocationStatusError, EvidenceKind: "error",
		Failure: &api.InvocationFailure{Code: "TOOL_OWNER_FAILED", Class: "owner_error"},
	}); err == nil {
		t.Fatal("second settlement succeeded")
	}
	items, err := recorder.ListSession(t.Context(), "session-1")
	testutil.FailErr(t, "list receipts", err)
	if len(items) != 1 || items[0].ContractDigest != contract.Digest() {
		t.Fatalf("items = %+v", items)
	}
}

func TestSQLRecorderInterruptsRunningReceipts(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	contract, ok := toolcontract.Lookup("read")
	if !ok {
		t.Fatal("read contract is not declared")
	}
	recorder := invocation.NewSQLRecorder(database)
	_, err := recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
		ToolCallID: "call-1", ToolName: "read", Args: map[string]any{"path": "README.md"}, Contract: contract,
	})
	testutil.FailErr(t, "begin receipt", err)
	count, err := recorder.InterruptRunning(t.Context())
	testutil.FailErr(t, "interrupt receipts", err)
	if count != 1 {
		t.Fatalf("interrupted = %d want 1", count)
	}
	items, err := recorder.ListSession(t.Context(), "session-1")
	testutil.FailErr(t, "list receipts", err)
	if len(items) != 1 || items[0].Status != api.InvocationStatusInterrupted || items[0].Evidence.Kind != "recovery" ||
		items[0].Failure == nil || items[0].Failure.Code != "TOOL_OWNER_INTERRUPTED" || !items[0].Failure.Retryable {
		t.Fatalf("items = %+v", items)
	}
}

func TestSQLRecorderInterruptsOnlyStoppedSessionReceipts(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	for _, sessionID := range []string{"session-stop", "session-live"} {
		testdbseed.InsertSession(t, database, sessionID, testdbseed.DefaultProjectID)
	}
	contract, ok := toolcontract.Lookup("read")
	if !ok {
		t.Fatal("read contract is not declared")
	}
	recorder := invocation.NewSQLRecorder(database)
	for _, sessionID := range []string{"session-stop", "session-live"} {
		_, err := recorder.Begin(t.Context(), invocation.Start{
			ProjectID:  testdbseed.DefaultProjectID,
			SessionID:  sessionID,
			ToolCallID: "call-" + sessionID,
			ToolName:   "read",
			Args:       map[string]any{"path": "README.md"},
			Contract:   contract,
		})
		testutil.FailErr(t, "begin receipt", err)
	}
	count, err := recorder.InterruptRunningSession(t.Context(), "session-stop")
	testutil.FailErr(t, "interrupt stopped session", err)
	if count != 1 {
		t.Fatalf("interrupted = %d, want one", count)
	}
	stopped, err := recorder.ListSession(t.Context(), "session-stop")
	testutil.FailErr(t, "list stopped session", err)
	live, err := recorder.ListSession(t.Context(), "session-live")
	testutil.FailErr(t, "list live session", err)
	if stopped[0].Status != api.InvocationStatusInterrupted || stopped[0].Evidence.Kind != "user_stop" {
		t.Fatalf("stopped receipt = %+v", stopped[0])
	}
	if live[0].Status != api.InvocationStatusRunning {
		t.Fatalf("other session receipt status = %q, want running", live[0].Status)
	}
}

func TestSQLRecorderPersistsTypedFailure(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract is not declared")
	}
	recorder := invocation.NewSQLRecorder(database)
	receipt, err := recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
		ToolCallID: "call-1", ToolName: "command", Args: map[string]any{"command": "false"}, Contract: contract,
	})
	testutil.FailErr(t, "begin receipt", err)
	settled, err := recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status: api.InvocationStatusError, Invoked: true, EvidenceKind: "error",
		Failure: &api.InvocationFailure{
			Code: "TOOL_OWNER_FAILED", Class: "owner_error", Retryable: false, OwnerRef: "process-1",
		},
	})
	testutil.FailErr(t, "settle failure", err)
	if settled.Failure == nil || settled.Failure.Class != "owner_error" || settled.Failure.OwnerRef != "process-1" {
		t.Fatalf("settled failure = %+v", settled)
	}
}

// A source verdict settles with the receipt and is read back from it, not from
// the result body.
func TestSQLRecorderPersistsStatedSourceVerdict(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	contract, ok := toolcontract.Lookup("verify")
	if !ok {
		t.Fatal("verify contract is not declared")
	}
	recorder := invocation.NewSQLRecorder(database)
	receipt, err := recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
		ToolCallID: "call-1", ToolName: "verify",
		Args: map[string]any{"command": "./task check"}, Contract: contract,
	})
	testutil.FailErr(t, "begin receipt", err)
	settled, err := recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status: api.InvocationStatusCompleted, Invoked: true, EvidenceKind: "attempt",
		SourceRevision: "boot-1:7", SourceRootDigest: "digest-1",
		SourceVerdict: api.SourceVerdictPassed,
	})
	testutil.FailErr(t, "settle verdict", err)
	if settled.SourceVerdict != api.SourceVerdictPassed {
		t.Fatalf("settled verdict = %q, want %q", settled.SourceVerdict, api.SourceVerdictPassed)
	}
	listed, err := recorder.ListSession(t.Context(), "session-1")
	testutil.FailErr(t, "list session receipts", err)
	if len(listed) != 1 || listed[0].SourceVerdict != api.SourceVerdictPassed {
		t.Fatalf("ledger verdict = %+v", listed)
	}
}

// The ledger accepts only terminal readings.
func TestSQLRecorderRefusesUnknownSourceVerdict(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	contract, ok := toolcontract.Lookup("verify")
	if !ok {
		t.Fatal("verify contract is not declared")
	}
	recorder := invocation.NewSQLRecorder(database)
	receipt, err := recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1",
		ToolCallID: "call-1", ToolName: "verify",
		Args: map[string]any{"command": "./task check"}, Contract: contract,
	})
	testutil.FailErr(t, "begin receipt", err)
	if _, err := recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{
		Status: api.InvocationStatusCompleted, Invoked: true, EvidenceKind: "attempt",
		SourceVerdict: "probably-fine",
	}); err == nil {
		t.Fatal("ledger accepted a verdict that is not a terminal reading")
	}
}
