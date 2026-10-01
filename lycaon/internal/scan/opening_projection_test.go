package scan

import (
	"fmt"
	"strings"
	"testing"
	"time"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBoardProjectionRetainsAuthorityBeyondFailedHistory(t *testing.T) {
	store := authorityTestStore(t)
	root := t.TempDir()
	for i := range 2 {
		s := authorityScan(fmt.Sprintf("complete-%d", i), fmt.Sprintf("assessment-%d", i), root,
			fmt.Sprintf("snapshot-%d", i), "sast", api.ScanTargetFull, nil)
		testutil.FailErr(t, "insert complete", store.Insert(t.Context(), s, nil, ""))
		completeNextScan(t, store, scanfindings.FixtureFinding("rule", api.FindingLevelHigh, "stable", "src/main.go", 4))
	}
	for i := range 40 {
		s := authorityScan(fmt.Sprintf("failed-%d", i), fmt.Sprintf("failed-assessment-%d", i), root,
			fmt.Sprintf("failed-snapshot-%d", i), "sast", api.ScanTargetFull, nil)
		testutil.FailErr(t, "insert attempt", store.Insert(t.Context(), s, nil, ""))
		_, err := store.FinalizePendingFailure(t.Context(), s.ID, "SCAN_ENGINE_FAILED", "failure")
		testutil.FailErr(t, "fail attempt", err)
	}
	view, err := store.AssessmentView(t.Context(), []string{root})
	testutil.FailErr(t, "read board view", err)
	if view.CurrentID != "assessment-1" || view.PreviousID != "assessment-0" || view.LatestAttemptID != "failed-assessment-39" {
		t.Fatalf("selected assessments = %+v", view)
	}
	if view.CurrentSummary.FindingsCount != 1 || view.CurrentSummary.TopLocations[0].URI != "src/main.go" {
		t.Fatalf("summary = %+v", view.CurrentSummary)
	}
	if len(view.Current[0].Findings) != 0 || len(view.Current[0].Result) != 0 {
		t.Fatal("board read hydrated full scan evidence")
	}
	comparison, err := (&CoordinatorImpl{Store: store}).BoardComparison(t.Context(), view.Previous[0], view.Current[0])
	testutil.FailErr(t, "read persisted comparison", err)
	if comparison.PersistedCount != 1 {
		t.Fatalf("comparison = %+v", comparison)
	}
}

func TestAssessmentHotspotsAggregateBeforeCapping(t *testing.T) {
	var parts []findingRollup
	for scanner := range 2 {
		var findings []api.SecurityFinding
		for i := range 6 {
			findings = append(findings, scanfindings.FixtureFinding("rule", api.FindingLevelHigh,
				fmt.Sprintf("%d-%d", scanner, i), fmt.Sprintf("%d-%d.go", scanner, i), 1))
		}
		findings = append(findings, scanfindings.FixtureFinding("shared", api.FindingLevelHigh, "shared", "z-shared.go", 2))
		parts = append(parts, rollupFindings(findings))
	}
	merged := mergeFindingRollups(parts)
	if merged.Count != 14 || merged.TopLocations[0].URI != "z-shared.go" {
		t.Fatalf("aggregate = %+v", merged)
	}
}

func BenchmarkAssessmentViewLargeHistory(b *testing.B) {
	for _, history := range []int{100, 1000, 3000} {
		b.Run(fmt.Sprintf("history-%d", history), func(b *testing.B) {
			benchmarkAssessmentView(b, history)
		})
	}
}

func benchmarkAssessmentView(b *testing.B, history int) {
	store := NewSQLStore(testdbfixture.Open(b, "store.db"))
	root := b.TempDir()
	findings := make([]api.SecurityFinding, 20000)
	for i := range findings {
		findings[i] = scanfindings.FixtureFinding("rule", api.FindingLevelHigh, fmt.Sprintf("finding-%d", i), fmt.Sprintf("src/file-%d.go", i), 1)
	}
	for i := range history {
		s := authorityScan(fmt.Sprintf("scan-%d", i), fmt.Sprintf("assessment-%d", i), root,
			fmt.Sprintf("snapshot-%d", i), "sast", api.ScanTargetFull, nil)
		s.CreatedAt = time.Now().Add(time.Duration(i) * time.Second)
		if err := store.Insert(b.Context(), s, nil, ""); err != nil {
			b.Fatalf("insert: %v", err)
		}
		claimed, err := store.ClaimNext(b.Context())
		if err != nil {
			b.Fatalf("claim: %v", err)
		}
		if i < 2 {
			if _, err := store.MarkComplete(b.Context(), claimed, &scanoutput.Result{FindingsCount: len(findings), Findings: findings}); err != nil {
				b.Fatalf("complete: %v", err)
			}
		} else {
			if _, err := store.FinalizeFailure(b.Context(), claimed, api.CodeScanStatusFailed, "SCAN_ENGINE_FAILED", "failure"); err != nil {
				b.Fatalf("fail: %v", err)
			}
		}
		if i < 2 {
			_, err = store.db.ExecContext(b.Context(), "UPDATE code_scans SET ingest_json = ? WHERE id = ?", `{"unused":"`+strings.Repeat("x", 4<<20)+`"}`, s.ID)
			if err != nil {
				b.Fatalf("seed historical payload: %v", err)
			}
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := store.AssessmentView(b.Context(), []string{root}); err != nil {
			b.Fatalf("board: %v", err)
		}
	}
}

func TestOpeningProjectionsKeepEvidenceAndWarningCounts(t *testing.T) {
	store := authorityTestStore(t)
	root := t.TempDir()
	warning := api.ScanWarning{Kind: api.ScanWarningFilePartialParse, File: "src/main.go", RuleID: "rule"}
	for _, engine := range []string{"sast", "sca"} {
		s := authorityScan(engine, "assessment", root, "snapshot", engine, api.ScanTargetFull, nil)
		testutil.FailErr(t, "insert scan", store.Insert(t.Context(), s, nil, ""))
		claimed, err := store.ClaimNext(t.Context())
		testutil.FailErr(t, "claim scan", err)
		finding := scanfindings.FixtureFinding("rule", api.FindingLevelHigh, engine, "src/main.go", 4)
		_, err = store.MarkComplete(t.Context(), claimed, &scanoutput.Result{FindingsCount: 1,
			Findings: []api.SecurityFinding{finding}, Warnings: []api.ScanWarning{warning}})
		testutil.FailErr(t, "complete scan", err)
	}
	// Opening projections read aggregates without decoding detail payloads.
	_, err := store.db.ExecContext(t.Context(), `UPDATE scan_summaries SET summary_json = '[]'`)
	testutil.FailErr(t, "isolate list projection", err)
	_, err = store.db.ExecContext(t.Context(), `UPDATE scan_finding_sets SET warnings_json = '[1]'`)
	testutil.FailErr(t, "isolate warning projection", err)
	view, err := store.AssessmentView(t.Context(), []string{root})
	testutil.FailErr(t, "read assessment", err)
	if view.LatestSummary.FindingsCount != 2 || len(view.LatestSummary.WarningSummary) != 1 || view.LatestSummary.WarningSummary[0].Count != 1 {
		t.Fatalf("assessment lost aggregate facts: %+v", view.LatestSummary)
	}
	page, err := store.ListPageByCanonicalPaths(t.Context(), []string{root}, PageQuery{Limit: 1})
	testutil.FailErr(t, "read scan list", err)
	if len(page.Scans) != 1 || len(page.Scans[0].Guidance) != 0 || len(page.Scans[0].Findings) != 0 || len(page.Scans[0].Warnings) != 0 {
		t.Fatalf("list hydrated detail: %+v", page)
	}
	row := page.Scans[0]
	if row.FindingsCount != 1 || len(row.TopLocations) != 1 || row.TopLocations[0].URI != "src/main.go" || len(row.WarningSummary) != 1 {
		t.Fatalf("list lost summary facts: %+v", row)
	}
	lines := WorkerScanDigest(t.Context(), &CoordinatorImpl{Store: store}, root, time.Now())
	if !strings.Contains(strings.Join(lines, "\n"), "src/main.go") {
		t.Fatalf("worker lost finding locations: %v", lines)
	}
}
