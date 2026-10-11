package scanning

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScanTerminalAcknowledgementSurvivesFailedWorkflowDelivery(t *testing.T) {
	database := testdbfixture.Open(t, "scan-delivery.db")
	store := scan.NewSQLStore(database)
	for _, run := range []string{"first", "second"} {
		testdbseed.InsertWorkflowRun(t, database, run, run+"-session", testdbseed.DefaultProjectID)
	}
	testutil.FailErr(t, "insert completed scan", store.Insert(t.Context(), api.CodeScan{ID: "scan", CanonicalPath: "/fixture", ScannerID: "fixture", Status: api.CodeScanStatusComplete}, nil, ""))
	for _, run := range []string{"first", "second"} {
		testutil.FailErr(t, "bind scan to workflow", store.BindWorkflowRun(t.Context(), "scan", run))
	}
	failure := errors.New("workflow store unavailable")
	err := ReconcileTerminals(t.Context(), store, "scan", func(_ context.Context, run, kind string) error {
		if kind != scan.WorkflowObligationKind {
			t.Fatalf("terminal kind=%q", kind)
		}
		if run == "first" {
			return failure
		}
		return nil
	})
	if !errors.Is(err, failure) {
		t.Fatalf("terminal delivery=%v", err)
	}
	pending, err := store.PendingTerminalWorkflowBindings(t.Context(), "scan", 10)
	testutil.FailErr(t, "read retryable delivery", err)
	if len(pending) != 1 || pending[0].WorkflowRunID != "first" {
		t.Fatalf("pending delivery=%+v", pending)
	}
	calls := 0
	testutil.FailErr(t, "retry workflow delivery", ReconcileTerminals(t.Context(), store, "scan", func(_ context.Context, run, _ string) error {
		calls++
		if run != "first" {
			t.Fatalf("already acknowledged run redelivered:%q", run)
		}
		return nil
	}))
	pending, err = store.PendingTerminalWorkflowBindings(t.Context(), "scan", 10)
	testutil.FailErr(t, "read acknowledged delivery", err)
	if calls != 1 || len(pending) != 0 {
		t.Fatalf("retry calls=%d pending=%+v", calls, pending)
	}
}
