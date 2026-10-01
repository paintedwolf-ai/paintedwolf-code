package integration

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// A superseded scan has no scanner outcome.
func TestPendingSupersededScanHasExplicitTerminalStatus(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, staticHead{sha: "deadbeef"})
	ctx := context.Background()
	dir := testProjectDir(t)

	winner, err := coord.Enqueue(ctx, scan.EnqueueRequest{
		ProjectDir: dir,
		Categories: []api.ScanCategory{api.ScanCategorySAST},
		ScannerID:  "lycaon-sast",
		Trigger:    api.ScanTriggerManual,
	})
	testutil.FailErr(t, "winner enqueue", err)

	loser, err := coord.Enqueue(ctx, scan.EnqueueRequest{
		ProjectDir: dir,
		Categories: []api.ScanCategory{api.ScanCategorySAST},
		ScannerID:  "lycaon-sast",
		Trigger:    api.ScanTriggerManual,
	})
	testutil.FailErr(t, "loser enqueue", err)
	if loser.ID != winner.ID {
		t.Fatalf("dedup returned a second scan: %s vs %s", loser.ID, winner.ID)
	}

	all, err := store.ListByCanonicalPath(ctx, winner.CanonicalPath)
	testutil.FailErr(t, "list", err)
	for _, s := range all {
		if s.ID == winner.ID {
			continue
		}
		if s.Status == api.CodeScanStatusFailed {
			t.Fatalf("superseded scan %s recorded as failed: %q", s.ID, s.Error)
		}
		if s.Status != api.CodeScanStatusSuperseded {
			t.Fatalf("superseded scan %s status = %q, want %q", s.ID, s.Status, api.CodeScanStatusSuperseded)
		}
		if s.ReplacementScanID != winner.ID {
			t.Fatalf("superseded scan %s replacement = %q, want %q", s.ID, s.ReplacementScanID, winner.ID)
		}
	}
}

// Supersession redirects every consumer binding.
func TestMarkPendingSupersededNamesItsSuccessor(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := scan.NewSQLStore(sqlDB)
	ctx := context.Background()
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-1", "session-1", testdbseed.DefaultProjectID)
	canonicalPath, err := scan.CanonicalPath(testProjectDir(t))
	testutil.FailErr(t, "canonical path", err)
	pending := api.CodeScan{
		ID:               "pending-scan-id",
		CanonicalPath:    canonicalPath,
		Categories:       []api.ScanCategory{api.ScanCategorySecurity},
		ScannerID:        "lycaon-sast",
		Status:           api.CodeScanStatusPending,
		SourceSnapshotID: api.SourceSnapshotWarming,
		Trigger:          api.ScanTriggerManual,
		WorkflowRunID:    "run-1",
		SessionID:        "session-1",
	}
	testutil.FailErr(t, "insert pending", store.Insert(ctx, pending, nil, ""))
	pendingRow, err := store.Get(ctx, pending.ID)
	testutil.FailErr(t, "load pending", err)
	pending = *pendingRow
	successor := pending
	successor.ID = "winner-scan-id"
	successor.Status = api.CodeScanStatusComplete
	successor.SourceSnapshotID = "published-snapshot"
	successor.AssessmentID = ""
	successor.WorkflowRunID = ""
	successor.SessionID = ""
	testutil.FailErr(t, "insert successor", store.Insert(ctx, successor, successor.TargetPaths, ""))

	won, err := store.MarkPendingSuperseded(ctx, pending.ID, "winner-scan-id")
	testutil.FailErr(t, "MarkPendingSuperseded", err)
	if !won {
		t.Fatal("MarkPendingSuperseded did not apply to a pending scan")
	}

	got, err := store.Get(ctx, pending.ID)
	testutil.FailErr(t, "get", err)
	if got.Status != api.CodeScanStatusSuperseded {
		t.Fatalf("status = %q, want %q", got.Status, api.CodeScanStatusSuperseded)
	}
	if got.ReplacementScanID != "winner-scan-id" {
		t.Fatalf("replacement = %q", got.ReplacementScanID)
	}
	byRun, err := store.ListByWorkflowRunID(ctx, "run-1")
	testutil.FailErr(t, "list transferred workflow binding", err)
	if len(byRun) != 1 || byRun[0].ID != successor.ID {
		t.Fatalf("workflow scans after supersession = %#v", byRun)
	}
	bySession, err := store.ListBySessionID(ctx, "session-1")
	testutil.FailErr(t, "list transferred session binding", err)
	if len(bySession) != 1 || bySession[0].ID != successor.ID {
		t.Fatalf("session scans after supersession = %#v", bySession)
	}
	pendingDelivery, err := store.PendingTerminalWorkflowBindings(ctx, successor.ID, 10)
	testutil.FailErr(t, "list successor delivery", err)
	if len(pendingDelivery) != 1 || pendingDelivery[0].WorkflowRunID != "run-1" {
		t.Fatalf("successor terminal delivery = %#v", pendingDelivery)
	}
	assessment, err := db.New(sqlDB).GetSecurityAssessmentIdentity(ctx, pending.AssessmentID)
	testutil.FailErr(t, "load transferred assessment", err)
	if assessment.SourceSnapshotID != successor.SourceSnapshotID {
		t.Fatalf("transferred assessment snapshot = %q, want %q", assessment.SourceSnapshotID, successor.SourceSnapshotID)
	}
	mismatch := pending
	mismatch.ID = "different-snapshot-scan"
	mismatch.SourceSnapshotID = "different-snapshot"
	mismatch.AssessmentID = ""
	mismatch.WorkflowRunID = ""
	mismatch.SessionID = ""
	testutil.FailErr(t, "insert different snapshot", store.Insert(ctx, mismatch, mismatch.TargetPaths, ""))
	if _, err := store.MarkPendingSuperseded(ctx, mismatch.ID, successor.ID); err == nil {
		t.Fatal("supersession accepted a different source snapshot")
	}
	if _, err := store.MarkPendingSuperseded(ctx, pending.ID, ""); err == nil {
		t.Fatal("an unnamed successor was accepted")
	}
}
