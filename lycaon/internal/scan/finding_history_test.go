package scan

import (
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFindingHistoryAndLedgerRollBackWithoutTheirEvent(t *testing.T) {
	store := authorityTestStore(t)
	root := t.TempDir()
	testdbseed.InsertProjectRoot(t, store.db, testdbseed.DefaultProjectID, root)
	first := authorityScan("history-event", "assessment-event", root, "snapshot-event", "sast", api.ScanTargetFull, nil)
	testutil.FailErr(t, "insert scan", store.Insert(t.Context(), first, nil, ""))
	finding := scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "finding", "a.go", 1)
	completed := completeNextScan(t, store, finding)
	outbox := &progressOutbox{err: errors.New("outbox unavailable")}
	store.SetEventOutbox(outbox)
	at := time.Now().UTC()
	if err := store.RecordFindingEvents(t.Context(), &completed, []api.SecurityFinding{finding}, nil, at); !errors.Is(err, outbox.err) {
		t.Fatalf("history write error = %v, want %v", err, outbox.err)
	}
	open, err := store.OpenFindings(t.Context(), root, "sast")
	testutil.FailErr(t, "read rolled-back history", err)
	subjects, err := store.queries.ListScanFindingLedgerSubjects(t.Context(), root)
	testutil.FailErr(t, "read rolled-back ledger", err)
	if len(open) != 0 || len(subjects) != 0 {
		t.Fatalf("event failure committed history or ledger: %d/%d", len(open), len(subjects))
	}
	outbox.err = nil
	testutil.FailErr(t, "commit history with event", store.RecordFindingEvents(t.Context(), &completed, []api.SecurityFinding{finding}, nil, at))
	open, err = store.OpenFindings(t.Context(), root, "sast")
	testutil.FailErr(t, "read committed history", err)
	if len(open) != 1 || len(outbox.events) != 1 || outbox.events[0].ScanID != completed.ID {
		t.Fatalf("history/event commit = %d/%+v", len(open), outbox.events)
	}
}

func TestFindingHistoryRecordsIntroductionsAndFixes(t *testing.T) {
	store := authorityTestStore(t)
	root := t.TempDir()
	first := authorityScan("scan-1", "assessment-1", root, "snapshot-1", "sast", api.ScanTargetPaths, nil)
	testutil.FailErr(t, "insert first", store.Insert(t.Context(), first, []string{"a.go"}, ""))
	a := scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "a", "a.go", 1)
	b := scanfindings.FixtureFinding("rule-b", api.FindingLevelLow, "b", "a.go", 2)
	completed := completeNextScan(t, store, a, b)
	t0 := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	testutil.FailErr(t, "record introductions", store.RecordFindingEvents(t.Context(), &completed, []api.SecurityFinding{a, b}, nil, t0))

	open, err := store.OpenFindings(t.Context(), root, "sast")
	testutil.FailErr(t, "open findings", err)
	if len(open) != 2 {
		t.Fatalf("open = %d, want 2", len(open))
	}

	second := authorityScan("scan-2", "assessment-2", root, "snapshot-2", "sast", api.ScanTargetPaths, nil)
	testutil.FailErr(t, "insert second", store.Insert(t.Context(), second, []string{"a.go"}, "snapshot-1"))
	done := completeNextScan(t, store, a)
	t1 := t0.Add(time.Minute)
	testutil.FailErr(t, "record fix", store.RecordFindingEvents(t.Context(), &done, nil, []api.SecurityFinding{b}, t1))

	open, err = store.OpenFindings(t.Context(), root, "sast")
	testutil.FailErr(t, "open findings after fix", err)
	if len(open) != 1 {
		t.Fatalf("open after fix = %d, want 1", len(open))
	}
	if _, stillOpen := open[FindingIdentity(b)]; stillOpen {
		t.Fatal("a fixed finding stayed open")
	}
	fixed, err := store.FixedFindingsSince(t.Context(), root, "sast", t0)
	testutil.FailErr(t, "fixed since", err)
	if len(fixed) != 1 || fixed[0].RuleID != "rule-b" {
		t.Fatalf("fixed since = %+v", fixed)
	}
	if n, err := store.IntroducedSince(t.Context(), root, t0, api.FindingLevelHigh); err != nil || n != 1 {
		t.Fatalf("introduced at or above high since t0 = %d, %v; want 1", n, err)
	}

	loaded, err := store.Get(t.Context(), completed.ID)
	testutil.FailErr(t, "reload first scan", err)
	if len(loaded.Findings) != 2 {
		t.Fatalf("first scan findings = %d", len(loaded.Findings))
	}
	for _, finding := range loaded.Findings {
		if finding.History == nil || !finding.History.IntroducedAt.Equal(t0) || finding.History.IntroducedScanID != completed.ID {
			t.Fatalf("finding %s history = %+v, want introduced at t0 by the first scan", finding.RuleID, finding.History)
		}
	}
}

func TestScanQueryReturnsFixesWhenNoCurrentFindingsRemain(t *testing.T) {
	store := authorityTestStore(t)
	root := t.TempDir()
	first := authorityScan("before", "assessment-before", root, "snapshot-before", "sast", api.ScanTargetFull, nil)
	testutil.FailErr(t, "insert first scan", store.Insert(t.Context(), first, nil, ""))
	finding := scanfindings.FixtureFinding("rule", api.FindingLevelHigh, "finding", "a.go", 1)
	before := completeNextScan(t, store, finding)
	since := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	testutil.FailErr(t, "record introduction", store.RecordFindingEvents(t.Context(), &before, []api.SecurityFinding{finding}, nil, since))
	second := authorityScan("after", "assessment-after", root, "snapshot-after", "sast", api.ScanTargetFull, nil)
	testutil.FailErr(t, "insert second scan", store.Insert(t.Context(), second, nil, ""))
	after := completeNextScan(t, store)
	testutil.FailErr(t, "record fix", store.RecordFindingEvents(t.Context(), &after, nil, []api.SecurityFinding{finding}, since.Add(time.Minute)))
	coordinator := newTestCoordinator(t, store, nil)
	out, err := coordinator.Query(t.Context(), QueryRequest{ScanID: after.ID, FixedSince: &since})
	testutil.FailErr(t, "query empty current findings", err)
	if len(out.Findings) != 0 || out.TotalMatch != 0 || len(out.FixedFindings) != 1 || out.FixedFindings[0].RuleID != "rule" {
		t.Fatalf("fixed finding missing from empty current results: %+v", out)
	}
}

func TestFindingHistoryReintroductionAfterAFixIsANewIntroduction(t *testing.T) {
	store := authorityTestStore(t)
	root := t.TempDir()
	scan := authorityScan("scan-1", "assessment-1", root, "snapshot-1", "sast", api.ScanTargetPaths, nil)
	testutil.FailErr(t, "insert", store.Insert(t.Context(), scan, []string{"a.go"}, ""))
	a := scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "a", "a.go", 1)
	completed := completeNextScan(t, store, a)
	t0 := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	testutil.FailErr(t, "introduce", store.RecordFindingEvents(t.Context(), &completed, []api.SecurityFinding{a}, nil, t0))
	fixedScan := completed
	fixedScan.SourceSnapshotID = "snapshot-2"
	testutil.FailErr(t, "fix", store.RecordFindingEvents(t.Context(), &fixedScan, nil, []api.SecurityFinding{a}, t0.Add(time.Minute)))
	againScan := completed
	againScan.SourceSnapshotID = "snapshot-3"
	testutil.FailErr(t, "reintroduce", store.RecordFindingEvents(t.Context(), &againScan, []api.SecurityFinding{a}, nil, t0.Add(2*time.Minute)))
	open, err := store.OpenFindings(t.Context(), root, "sast")
	testutil.FailErr(t, "open", err)
	if len(open) != 1 {
		t.Fatalf("open after reintroduction = %d, want 1", len(open))
	}
}

func TestBlobFindingsCacheRoundTrip(t *testing.T) {
	store := authorityTestStore(t)
	if _, ok, err := store.BlobFindings(t.Context(), "fp", "sha", "a.go"); err != nil || ok {
		t.Fatalf("empty cache = %v %v", ok, err)
	}
	finding := scanfindings.FixtureFinding("rule", api.FindingLevelHigh, "x", "file.go", 1)
	testutil.FailErr(t, "save", store.SaveBlobFindings(t.Context(), "fp", []CachedFileResult{{ContentID: "sha", Path: "a.go", Findings: []api.SecurityFinding{finding}}}, time.Now()))
	cached, ok, err := store.BlobFindings(t.Context(), "fp", "sha", "a.go")
	testutil.FailErr(t, "read", err)
	if !ok || len(cached) != 1 || cached[0].RuleID != "rule" {
		t.Fatalf("cached = %+v ok=%v", cached, ok)
	}
	testutil.FailErr(t, "save clean", store.SaveBlobFindings(t.Context(), "fp", []CachedFileResult{{ContentID: "clean", Path: "a.go"}}, time.Now()))
	cached, ok, err = store.BlobFindings(t.Context(), "fp", "clean", "a.go")
	testutil.FailErr(t, "read clean", err)
	if !ok || len(cached) != 0 {
		t.Fatalf("a clean file must cache as an empty list, got %+v ok=%v", cached, ok)
	}
}

func TestBlobFindingsReportsCorruptCache(t *testing.T) {
	store := authorityTestStore(t)
	testutil.FailErr(t, "seed corrupt cache", store.queries.UpsertScanBlobFindings(t.Context(), db.UpsertScanBlobFindingsParams{
		ExecutionFingerprint: "fp", ContentID: "sha", TargetPath: "a.go", FindingsJson: "{}", CreatedAt: db.FormatTime(time.Now()),
	}))
	findings, present, err := store.BlobFindings(t.Context(), "fp", "sha", "a.go")
	if err == nil || present || len(findings) != 0 {
		t.Fatalf("corrupt cache: findings=%v present=%v err=%v", findings, present, err)
	}
}
