//go:build integration

package cadence

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// Changed-file work occupies the scanner during full-pass requests.
func occupyScanner(t *testing.T, store *scanbase.SQLStore, canonical, scannerID string) string {
	t.Helper()
	busy := authorityScan("busy-"+scannerID, "busy-"+scannerID, canonical, "busy-snapshot", scannerID, api.ScanTargetPaths, nil)
	busy.Trigger = api.ScanTriggerWriteBurst
	testutil.FailErr(t, "occupy "+scannerID, store.Insert(t.Context(), busy, []string{"main.go"}, ""))
	return busy.ID
}

func finishBusyScan(t *testing.T, cadence *Service, store *scanbase.SQLStore, scanID string) {
	t.Helper()
	claimed, err := store.ClaimNext(t.Context())
	testutil.FailErr(t, "claim busy scan", err)
	if claimed.ID != scanID {
		t.Fatalf("claimed %s, want the busy scan %s", claimed.ID, scanID)
	}
	won, err := store.MarkComplete(t.Context(), claimed, &scanoutput.Result{})
	testutil.FailErr(t, "complete busy scan", err)
	if !won {
		t.Fatalf("busy scan %s completion lost", scanID)
	}
	completed, err := store.Get(t.Context(), scanID)
	testutil.FailErr(t, "read busy scan", err)
	cadence.OnTerminal(t.Context(), *completed)
	cadence.Tick(t.Context())
}

func memberPhases(pass scanbase.FullPass) map[string]api.FullPassMemberPhase {
	out := make(map[string]api.FullPassMemberPhase, len(pass.Members))
	for _, member := range pass.Members {
		out[member.ScannerID] = member.Phase
	}
	return out
}

func requireOneGeneration(t *testing.T, pass scanbase.FullPass) {
	t.Helper()
	snapshot := ""
	for _, member := range pass.Members {
		scan := member.Scan
		if member.Phase != api.FullPassMemberStarted || scan == nil {
			t.Fatalf("member %s = %+v, want started", member.ScannerID, member)
		}
		if scan.AssessmentID != pass.ID || scan.TargetKind != api.ScanTargetFull {
			t.Fatalf("member scan %+v, want a full scan of pass %s", scan, pass.ID)
		}
		if snapshot == "" {
			snapshot = scan.SourceSnapshotID
		}
		if scan.SourceSnapshotID != snapshot {
			t.Fatalf("pass members read %q and %q, want one generation", snapshot, scan.SourceSnapshotID)
		}
	}
}

func attachedCadence(t *testing.T) (*Service, *scanbase.SQLStore, string, string) {
	t.Helper()
	cadence, store := newTestCadence(t, newTestClock())
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)
	testutil.FailErr(t, "attach", cadence.BaselineRoot(t.Context(), dir))
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "canonical path", err)
	return cadence, store, dir, canonical
}

func TestFullPassWaitsForABusyScannerThenStartsTogether(t *testing.T) {
	cadence, store, dir, canonical := attachedCadence(t)
	busy := occupyScanner(t, store, canonical, "lycaon-sast")

	pass, err := cadence.RequestFull(t.Context(), dir, nil, api.ScanTriggerManual, scanbase.FullScanContext{})
	testutil.FailErr(t, "request full", err)
	if pass.Started() || len(pass.ScanIDs()) != 0 || pass.Finished() {
		t.Fatalf("pass started while a member was busy: %+v", pass)
	}
	want := map[string]api.FullPassMemberPhase{
		"lycaon-sast":    api.FullPassMemberWaitingForScanner,
		"lycaon-sca":     api.FullPassMemberWaitingForPass,
		"lycaon-secrets": api.FullPassMemberWaitingForPass,
	}
	if got := memberPhases(pass); len(got) != len(want) || got["lycaon-sast"] != want["lycaon-sast"] ||
		got["lycaon-sca"] != want["lycaon-sca"] || got["lycaon-secrets"] != want["lycaon-secrets"] {
		t.Fatalf("waiting phases = %v, want %v", got, want)
	}
	due, err := store.NextSeriesDue(t.Context())
	testutil.FailErr(t, "next due", err)
	if !due.IsZero() {
		t.Fatalf("a waiting pass reads as due at %q; the cadence would spin", due)
	}
	waiting, err := cadence.SecurityOverview(t.Context(), dir, nil)
	testutil.FailErr(t, "overview while waiting", err)
	if waiting.Running == nil || waiting.Running.AssessmentID != pass.ID || waiting.Running.StartedAt != nil {
		t.Fatalf("overview while waiting = %+v", waiting.Running)
	}
	owed, err := store.FullPassOwed(t.Context(), canonical)
	testutil.FailErr(t, "pass owed", err)
	if !owed {
		t.Fatal("a waiting pass must keep scan_done from reading as settled")
	}

	finishBusyScan(t, cadence, store, busy)
	started, err := store.FullPass(t.Context(), pass.ID)
	testutil.FailErr(t, "read started pass", err)
	if !started.Started() || len(started.ScanIDs()) != 3 {
		t.Fatalf("pass after the busy scanner finished = %+v, want every member started", started)
	}
	requireOneGeneration(t, *started)

	completeCadenceScans(t, cadence, store)
	after, err := cadence.SecurityOverview(t.Context(), dir, nil)
	testutil.FailErr(t, "overview after", err)
	if after.Running != nil || after.LastFull == nil || after.LastFull.AssessmentID != pass.ID || after.LastFull.CompletedAt == nil {
		t.Fatalf("overview after the pass = running %+v last %+v", after.Running, after.LastFull)
	}
	owed, err = store.FullPassOwed(t.Context(), canonical)
	testutil.FailErr(t, "pass owed after", err)
	if owed {
		t.Fatal("a finished pass is still owed")
	}
}

func TestFullPassRequestWidensAPassThatHasNotStarted(t *testing.T) {
	cadence, store, dir, canonical := attachedCadence(t)
	busy := occupyScanner(t, store, canonical, "lycaon-sast")

	first, err := cadence.RequestFull(t.Context(), dir, []string{"lycaon-sast"}, api.ScanTriggerManual, scanbase.FullScanContext{})
	testutil.FailErr(t, "first request", err)
	second, err := cadence.RequestFull(t.Context(), dir, []string{"lycaon-sca"}, api.ScanTriggerScanPack, scanbase.FullScanContext{})
	testutil.FailErr(t, "second request", err)
	if second.ID != first.ID {
		t.Fatalf("second request recorded pass %s, want it to widen %s", second.ID, first.ID)
	}
	if got := strings.Join(second.ScannerIDs(), ","); got != "lycaon-sast,lycaon-sca" {
		t.Fatalf("widened members = %s", got)
	}

	finishBusyScan(t, cadence, store, busy)
	started, err := store.FullPass(t.Context(), first.ID)
	testutil.FailErr(t, "read widened pass", err)
	requireOneGeneration(t, *started)
	for _, member := range started.Members {
		if member.Scan.Trigger != first.Trigger {
			t.Fatalf("member %s trigger = %s, want pass trigger %s", member.ScannerID, member.Scan.Trigger, first.Trigger)
		}
	}
}

func TestFullPassRequestBeyondAStartedPassRecordsAnotherThatWaits(t *testing.T) {
	cadence, store, dir, _ := attachedCadence(t)

	first, err := cadence.RequestFull(t.Context(), dir, []string{"lycaon-sast"}, api.ScanTriggerManual, scanbase.FullScanContext{})
	testutil.FailErr(t, "first request", err)
	if !first.Started() {
		t.Fatalf("an idle scanner's pass did not start: %+v", first)
	}
	joined, err := cadence.RequestFull(t.Context(), dir, []string{"lycaon-sast"}, api.ScanTriggerScanPack, scanbase.FullScanContext{})
	testutil.FailErr(t, "covered request", err)
	if joined.ID != first.ID {
		t.Fatalf("a covered request recorded pass %s, want it to join %s", joined.ID, first.ID)
	}

	second, err := cadence.RequestFull(t.Context(), dir, nil, api.ScanTriggerManual, scanbase.FullScanContext{})
	testutil.FailErr(t, "wider request", err)
	if second.ID == first.ID || second.Started() {
		t.Fatalf("a wider request = %+v, want a new pass waiting for the running one", second)
	}
	if phase := memberPhases(second)["lycaon-sast"]; phase != api.FullPassMemberWaitingForScanner {
		t.Fatalf("sast phase = %s, want it waiting for its running scan", phase)
	}
	overview, err := cadence.SecurityOverview(t.Context(), dir, nil)
	testutil.FailErr(t, "overview", err)
	if overview.Running == nil || overview.Running.AssessmentID != second.ID {
		t.Fatalf("overview running = %+v, want the newest pass", overview.Running)
	}

	completeCadenceScans(t, cadence, store)
	finished, err := store.FullPass(t.Context(), second.ID)
	testutil.FailErr(t, "read second pass", err)
	if !finished.Finished() {
		t.Fatalf("second pass did not finish: %+v", finished)
	}
	requireOneGeneration(t, *finished)
	for _, member := range finished.Members {
		if member.ScannerID == "lycaon-sast" && member.Scan.ID != first.ScanIDs()[0] {
			t.Fatalf("unchanged source reran sast: %s, want reused scan %s", member.Scan.ID, first.ScanIDs()[0])
		}
	}
	original, err := store.Get(t.Context(), first.ScanIDs()[0])
	testutil.FailErr(t, "read reused execution provenance", err)
	if original.AssessmentID != first.ID {
		t.Fatalf("stored execution assessment = %s, want original %s", original.AssessmentID, first.ID)
	}
}

func TestFullPassGoesOnWithoutAMemberDeselectedWhileWaiting(t *testing.T) {
	cadence, store, dir, canonical := attachedCadence(t)
	busy := occupyScanner(t, store, canonical, "lycaon-sast")
	pass, err := cadence.RequestFull(t.Context(), dir, nil, api.ScanTriggerManual, scanbase.FullScanContext{})
	testutil.FailErr(t, "request full", err)

	registry := cadence.Registry.(*scanbase.MockRegistry)
	kept := registry.Scanners[:0]
	for _, scanner := range registry.Scanners {
		if scanner.ID() != "lycaon-sca" {
			kept = append(kept, scanner)
		}
	}
	registry.Scanners = kept
	finishBusyScan(t, cadence, store, busy)
	completeCadenceScans(t, cadence, store)

	finished, err := store.FullPass(t.Context(), pass.ID)
	testutil.FailErr(t, "read pass", err)
	phases := memberPhases(*finished)
	if phases["lycaon-sca"] != api.FullPassMemberNotStarted ||
		phases["lycaon-sast"] != api.FullPassMemberStarted || phases["lycaon-secrets"] != api.FullPassMemberStarted {
		t.Fatalf("phases after deselection = %v", phases)
	}
	if !finished.Finished() {
		t.Fatalf("pass without its deselected member did not finish: %+v", finished)
	}
	if coverage := finished.Wire().CoverageStatus; coverage != api.ScanCoveragePartial {
		t.Fatalf("coverage = %s, want partial for a pass that ran less than it asked for", coverage)
	}
}

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

	// A requester that joins after the pass started binds its running scans too.
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
	var reject *tools.ToolReject
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
