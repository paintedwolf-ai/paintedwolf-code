package workflow

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestVerdictReceiptsPreserveSubmittedValuesAndPhaseIdentity(t *testing.T) {
	manager, _, blueprints, _ := testManager(t)
	setTestRegistry(t, manager, blueprints, conditions.TestRegistryDeps())
	run := startReviewLoopRun(t.Context(), t, manager)
	before := map[string]string{"verdict": "NEEDS_REVISION", "winner": "east"}
	after := map[string]string{"verdict": "SELECTED", "winner": "west"}
	_, err := manager.RecordReviewLoopVerdict(withVerdictOperationID(t.Context(), "z-first"), "sess-1", before, nil, nil)
	testutil.FailErr(t, "record correction", err)
	_, err = manager.RecordReviewLoopVerdict(withVerdictOperationID(t.Context(), "a-second"), "sess-1", after, nil, nil)
	testutil.FailErr(t, "record acceptance", err)
	store := manager.Store.(*SQLStore)
	_, err = store.db.ExecContext(t.Context(), `UPDATE workflow_verdict_operations SET created_at='2026-09-12T00:00:00Z' WHERE run_id=?`, run.ID)
	testutil.FailErr(t, "tie receipt timestamps", err)
	receipts, err := store.ReadVerdictReceipts(t.Context(), run.ID)
	testutil.FailErr(t, "read verdict receipts", err)
	if len(receipts) != 2 {
		t.Fatalf("receipts: %+v", receipts)
	}
	for i, receipt := range receipts {
		if receipt.RunID != run.ID || receipt.Phase != "judge" || receipt.Status != "committed" || receipt.SourceRevision < 1 || receipt.Submission.SessionID != "sess-1" {
			t.Fatalf("receipt identity: %+v", receipt)
		}
		if !receipt.Outcome.Valid || receipt.Outcome.Terminal != (i == 1) {
			t.Fatalf("receipt outcome: %+v", receipt)
		}
	}
	if receipts[0].Submission.Verdict["winner"] != "east" || receipts[1].Submission.Verdict["winner"] != "west" {
		t.Fatalf("submitted values: %+v", receipts)
	}
	if receipts[0].SourceRevision >= receipts[1].SourceRevision {
		t.Fatal("receipt revision order was lost")
	}
	unrelated, err := store.ReadVerdictReceipts(t.Context(), "other")
	testutil.FailErr(t, "read other workflow", err)
	if len(unrelated) != 0 {
		t.Fatal("workflow receipts leaked into another run")
	}
}

func TestVerdictReceiptsPreserveIncompleteOperations(t *testing.T) {
	for _, status := range []string{"prepared", "evidence_applied", "diverged"} {
		t.Run(status, func(t *testing.T) {
			manager, _, blueprints, _ := testManager(t)
			setTestRegistry(t, manager, blueprints, conditions.TestRegistryDeps())
			run := startReviewLoopRun(t.Context(), t, manager)
			store := manager.Store.(*SQLStore)
			_, _, err := store.prepareVerdictOperation(t.Context(), verdictOperation{
				ToolCallID: "pending-call", RunID: run.ID, SourceRevision: 1, Phase: "judge",
				InputDigest: "input", EvidenceRecordID: "pending-evidence",
				EvidenceJSON: `{"session_id":"sess-1","verdict":{"winner":"east"}}`,
			})
			testutil.FailErr(t, "prepare receipt", err)
			switch status {
			case "evidence_applied":
				_, err := store.db.ExecContext(t.Context(), "UPDATE workflow_verdict_operations SET status='evidence_applied' WHERE tool_call_id='pending-call'")
				testutil.FailErr(t, "seed released pending receipt", err)
			case "diverged":
				testutil.FailErr(t, "resolve divergent receipt", store.resolveVerdictOperationDiverged(t.Context(), "pending-call", "phase changed"))
			}
			receipts, err := store.ReadVerdictReceipts(t.Context(), run.ID)
			testutil.FailErr(t, "read incomplete receipts", err)
			if len(receipts) != 1 {
				t.Fatalf("incomplete receipts: %+v", receipts)
			}
			receipt := receipts[0]
			if receipt.Status != status || receipt.Submission.Verdict["winner"] != "east" || receipt.Outcome.Valid {
				t.Fatalf("incomplete receipt changed: %+v", receipt)
			}
		})
	}
}
