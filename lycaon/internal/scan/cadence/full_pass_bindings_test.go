//go:build integration

package cadence

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFullPassScansTakeTheBindingsOfEveryRequester(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "cadence.db")
	testdbseed.InsertSession(t, sqlDB, "session-a", testdbseed.DefaultProjectID)
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-b", "session-b", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, sqlDB, "session-c", testdbseed.DefaultProjectID)
	cadence, store := newTestCadenceOn(t, newTestClock(), sqlDB)
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)
	testutil.FailErr(t, "attach", cadence.BaselineRoot(t.Context(), dir))
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "canonical path", err)
	busy := occupyScanner(t, store, canonical, "lycaon-sast")

	first, err := cadence.RequestFull(t.Context(), dir, nil, api.ScanTriggerManual, scanbase.FullScanContext{SessionID: "session-a"})
	testutil.FailErr(t, "session request", err)
	second, err := cadence.RequestFull(t.Context(), dir, nil, api.ScanTriggerPhaseEnter, scanbase.FullScanContext{WorkflowRunID: "run-b", SessionID: "session-b"})
	testutil.FailErr(t, "workflow request", err)
	if second.ID != first.ID {
		t.Fatalf("workflow request recorded pass %s, want it to join %s", second.ID, first.ID)
	}
	passes, err := store.FullPassesForWorkflowRun(t.Context(), "run-b")
	testutil.FailErr(t, "passes for run", err)
	if len(passes) != 1 || passes[0].ID != first.ID || passes[0].Started() {
		t.Fatalf("run passes before start = %+v", passes)
	}

	finishBusyScan(t, cadence, store, busy)
	for label, list := range map[string]func() ([]api.CodeScan, error){
		"session-a": func() ([]api.CodeScan, error) { return store.ListBySessionID(t.Context(), "session-a") },
		"session-b": func() ([]api.CodeScan, error) { return store.ListBySessionID(t.Context(), "session-b") },
		"run-b":     func() ([]api.CodeScan, error) { return store.ListByWorkflowRunID(t.Context(), "run-b") },
	} {
		scans, err := list()
		testutil.FailErr(t, "list "+label+" scans", err)
		if len(scans) != 3 {
			t.Fatalf("%s holds %d scans, want the pass's three", label, len(scans))
		}
	}

	third, err := cadence.RequestFull(t.Context(), dir, []string{"lycaon-sast"}, api.ScanTriggerScanPack, scanbase.FullScanContext{SessionID: "session-c"})
	testutil.FailErr(t, "late request", err)
	late, err := store.ListBySessionID(t.Context(), "session-c")
	testutil.FailErr(t, "list late session scans", err)
	if third.ID != first.ID || len(late) != 3 {
		t.Fatalf("late requester joined %s holding %d scans, want %s with three", third.ID, len(late), first.ID)
	}
}

func TestFullPassRequestNeedsARunnableScanner(t *testing.T) {
	cadence, _, dir, _ := attachedCadence(t)
	cadence.Registry.(*scanbase.MockRegistry).Scanners = nil
	if _, err := cadence.RequestFull(t.Context(), dir, nil, api.ScanTriggerManual, scanbase.FullScanContext{}); !errors.Is(err, scanbase.ErrNoScannerAvailable) {
		t.Fatalf("request with no scanner = %v, want ErrNoScannerAvailable", err)
	}
}

func TestFullPassReceiptReadsMembersThatHaveNotStarted(t *testing.T) {
	cadence, store, dir, canonical := attachedCadence(t)
	occupyScanner(t, store, canonical, "lycaon-sast")
	pass, err := cadence.RequestFull(t.Context(), dir, nil, api.ScanTriggerScanPack, scanbase.FullScanContext{})
	testutil.FailErr(t, "request full", err)

	for _, read := range []func() (string, error){
		func() (string, error) {
			return scantoolapi.FullPassReceipt(t.Context(), cadence.Coordinator, pass, scantoolapi.ReceiptOptions{})
		},
		func() (string, error) {
			return scantoolapi.SummarizeFullPass(t.Context(), cadence.Coordinator, pass.ID, "summary", nil)
		},
	} {
		out, err := read()
		testutil.FailErr(t, "read pass receipt", err)
		var receipt scantoolapi.ScanPackToolResult
		testutil.FailErr(t, "decode receipt", json.Unmarshal([]byte(out), &receipt))
		if receipt.PassID != pass.ID || receipt.Status != api.CodeScanStatusPending || len(receipt.ScanIDs) != 0 {
			t.Fatalf("waiting receipt = %s", out)
		}
		if len(receipt.PerScan) != 3 {
			t.Fatalf("per_scan = %+v, want every member", receipt.PerScan)
		}
		for _, row := range receipt.PerScan {
			if row.PassPhase == "" || row.ScanID != "" {
				t.Fatalf("waiting member row = %+v", row)
			}
		}
		if !strings.Contains(receipt.Message, "scan_summary with this pass_id") {
			t.Fatalf("message = %q, want the pass_id follow-up", receipt.Message)
		}
	}

	_, err = scantoolapi.SummarizeFullPass(t.Context(), cadence.Coordinator, "missing-pass", "summary", nil)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != scanbase.DrilldownRejectPassNotFound || reject.Data["pass_id"] != "missing-pass" {
		t.Fatalf("unknown pass = %v, want %s", err, scanbase.DrilldownRejectPassNotFound)
	}
}

func TestFullPassListsOrderEqualRequestTimesByAdmission(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "full-pass-order.db")
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-order", "session-order", testdbseed.DefaultProjectID)
	store := scanbase.NewSQLStore(sqlDB)
	canonical := t.TempDir()
	requestedAt := newTestClock().Now()
	firstID := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	secondID := "00000000-0000-4000-8000-000000000001"
	for _, id := range []string{firstID, secondID} {
		testutil.FailErr(t, "insert full pass "+id, store.InsertFullPass(t.Context(), scanbase.FullPassDraft{
			ID: id, CanonicalPath: canonical, Scanners: []string{"lycaon-sast"},
			Trigger: api.ScanTriggerManual, RequestedAt: requestedAt,
		}))
	}
	for _, id := range []string{secondID, firstID} {
		testutil.FailErr(t, "bind full pass "+id, store.BindFullPassRequester(t.Context(), id,
			scanbase.FullScanContext{WorkflowRunID: "run-order"}))
	}
	byPath, err := store.FullPassesForPath(t.Context(), canonical)
	testutil.FailErr(t, "list root full passes", err)
	byRun, err := store.FullPassesForWorkflowRun(t.Context(), "run-order")
	testutil.FailErr(t, "list workflow full passes", err)
	for _, result := range []struct {
		name   string
		passes []scanbase.FullPass
	}{
		{name: "root", passes: byPath},
		{name: "workflow", passes: byRun},
	} {
		if len(result.passes) != 2 {
			t.Fatalf("%s pass count = %d, want 2", result.name, len(result.passes))
		}
		if result.passes[0].ID != secondID || result.passes[1].ID != firstID {
			t.Fatalf("%s order = [%s, %s], want admission order [%s, %s]", result.name,
				result.passes[0].ID, result.passes[1].ID, secondID, firstID)
		}
	}
}

func TestFullPassOwedForSessionCountsOnlyThePassesTheSessionRequested(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "full-pass-owed-session.db")
	testdbseed.InsertSession(t, sqlDB, "session-requester", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, sqlDB, "session-bystander", testdbseed.DefaultProjectID)
	store := scanbase.NewSQLStore(sqlDB)
	canonical := t.TempDir()
	passID := "11111111-1111-4111-8111-111111111111"
	testutil.FailErr(t, "insert full pass", store.InsertFullPass(t.Context(), scanbase.FullPassDraft{
		ID: passID, CanonicalPath: canonical, Scanners: []string{"lycaon-sast"},
		Trigger: api.ScanTriggerManual, RequestedAt: newTestClock().Now(),
	}))
	testutil.FailErr(t, "bind requester", store.BindFullPassRequester(t.Context(), passID,
		scanbase.FullScanContext{SessionID: "session-requester"}))
	series := scanbase.SeriesRow{
		CanonicalPath: canonical, ScannerID: "lycaon-sast", DesiredPassID: passID, DesiredTrigger: api.ScanTriggerManual,
	}
	testutil.FailErr(t, "owe the pass", store.UpsertSeries(t.Context(), series))

	for session, want := range map[string]bool{"session-requester": true, "session-bystander": false} {
		owed, err := store.FullPassOwedForSession(t.Context(), session)
		testutil.FailErr(t, "owed for "+session, err)
		if owed != want {
			t.Fatalf("pass owed to %s = %t, want %t", session, owed, want)
		}
	}
	if owed, err := store.FullPassOwed(t.Context(), canonical); err != nil || !owed {
		t.Fatalf("root pass owed = %t, %v; the root still owes it", owed, err)
	}

	series.DesiredPassID, series.DesiredTrigger = "", ""
	testutil.FailErr(t, "settle the pass", store.UpsertSeries(t.Context(), series))
	if owed, err := store.FullPassOwedForSession(t.Context(), "session-requester"); err != nil || owed {
		t.Fatalf("a settled pass is still owed to its requester: %t, %v", owed, err)
	}
}
