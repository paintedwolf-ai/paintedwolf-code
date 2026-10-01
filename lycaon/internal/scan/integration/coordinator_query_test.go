package integration

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func seedScanWithGuidance(t *testing.T, store *scan.SQLStore, delegationID string, guidance []api.ScanGuidanceSummary) string {
	t.Helper()
	coord := newTestCoordinator(t, store, nil)
	created, err := coord.Enqueue(context.Background(), scan.EnqueueRequest{
		ProjectDir:   t.TempDir(),
		Categories:   []api.ScanCategory{api.ScanCategorySecurity, api.ScanCategorySecret},
		DelegationID: delegationID,
	})
	testutil.FailErr(t, "coord.Enqueue failed", err)
	claimed := claimScan(t, store, created.ID)
	if _, err := store.MarkComplete(context.Background(), claimed, &scanoutput.Result{FindingsCount: len(guidance)}); err != nil {
		testutil.FailErr(t, "store.MarkComplete failed", err)
	}
	rec := evidence.Record{
		GateVerdict: string(evidence.GateVerdictPassed),
		Artifacts: map[string]any{
			"guidance": guidance,
		},
	}
	if err := store.SaveIngest(context.Background(), created.ID, rec); err != nil {
		testutil.FailErr(t, "store.SaveIngest failed", err)
	}
	return created.ID
}

func TestScanQueryFiltersFindingsByLevel(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	scanID := seedScanWithFindings(t, store, []api.SecurityFinding{
		scanfindings.FixtureFinding("r-high", api.FindingLevelHigh, "high", "a.go", 1),
		scanfindings.FixtureFinding("r-low", api.FindingLevelLow, "low", "b.go", 2),
	})

	resp, err := coord.Query(context.Background(), scan.QueryRequest{
		ScanID: scanID,
		Level:  "high",
	})
	testutil.FailErr(t, "coord.Query failed", err)
	if resp.TotalMatch != 1 || len(resp.Findings) != 1 || resp.Findings[0].Level != api.FindingLevelHigh {
		t.Fatalf("level filter = %#v", resp)
	}
}

func seedScanWithFindings(t *testing.T, store *scan.SQLStore, findings []api.SecurityFinding) string {
	t.Helper()
	coord := newTestCoordinator(t, store, nil)
	created, err := coord.Enqueue(context.Background(), scan.EnqueueRequest{
		ProjectDir: t.TempDir(),
		Categories: []api.ScanCategory{api.ScanCategorySecurity},
	})
	testutil.FailErr(t, "coord.Enqueue failed", err)
	claimed := claimScan(t, store, created.ID)
	if _, err := store.MarkComplete(context.Background(), claimed, &scanoutput.Result{
		FindingsCount: len(findings),
		Findings:      findings,
	}); err != nil {
		testutil.FailErr(t, "store.MarkComplete failed", err)
	}
	rec := evidence.Record{
		GateVerdict: string(evidence.GateVerdictPassed),
		Artifacts: map[string]any{
			"findings": findings,
			"guidance": []api.ScanGuidanceSummary{},
		},
	}
	if err := store.SaveIngest(context.Background(), created.ID, rec); err != nil {
		testutil.FailErr(t, "store.SaveIngest failed", err)
	}
	return created.ID
}

func TestScanQueryFiltersAndBudget(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	scanID := seedScanWithGuidance(t, store, "", []api.ScanGuidanceSummary{
		{Code: "SCAN_SQL_INJECTION", Message: "sql", RuleID: "lycaon.ruby.sql-string-concat", File: "app.rb", Severity: "ERROR"},
		{Code: "SCAN_HARDCODED_SECRET", Message: "secret", RuleID: "gitleaks:github-pat", File: ".env", Severity: "ERROR"},
		{Code: "SCAN_FINDING_UNMAPPED", Message: "other", RuleID: "vendor.other", File: "pkg/x.go", Severity: "WARNING"},
	})

	resp, err := coord.Query(context.Background(), scan.QueryRequest{
		ScanID: scanID,
		Code:   "SCAN_HARDCODED_SECRET",
	})
	testutil.FailErr(t, "coord.Query failed", err)
	if resp.TotalMatch != 1 || len(resp.Guidance) != 1 || resp.Guidance[0].Code != "SCAN_HARDCODED_SECRET" {
		t.Fatalf("filter by code = %#v", resp)
	}

	resp, err = coord.Query(context.Background(), scan.QueryRequest{
		ScanID: scanID,
		Limit:  1,
	})
	testutil.FailErr(t, "coord.Query failed", err)
	if resp.TotalMatch != 3 || !resp.Truncated || len(resp.Guidance) != 1 {
		t.Fatalf("limit = %#v", resp)
	}

	resp, err = coord.Query(context.Background(), scan.QueryRequest{
		ScanID: scanID,
		Cursor: resp.NextCursor,
		Limit:  1,
	})
	testutil.FailErr(t, "coord.Query offset", err)
	if resp.TotalMatch != 3 || !resp.Truncated || len(resp.Guidance) != 1 || resp.NextCursor == "" {
		t.Fatalf("offset page = %#v", resp)
	}

	resp, err = coord.Query(context.Background(), scan.QueryRequest{
		ScanID: scanID,
		Level:  string(api.FindingLevelHigh),
	})
	testutil.FailErr(t, "coord.Query failed", err)
	if resp.TotalMatch != 2 || len(resp.Guidance) != 2 {
		t.Fatalf("level filter = %#v", resp)
	}
}

func TestScanQueryPathFilterRequiresLocatedGuidance(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	scanID := seedScanWithGuidance(t, store, "", []api.ScanGuidanceSummary{
		{Code: "LOCATED", Message: "inside", RuleID: "r1", File: "src/main.go", Severity: "ERROR"},
		{Code: "OTHER", Message: "outside", RuleID: "r2", File: "tests/main.go", Severity: "ERROR"},
		{Code: "UNLOCATED", Message: "no file", RuleID: "r3", Severity: "ERROR"},
	})

	resp, err := coord.Query(context.Background(), scan.QueryRequest{ScanID: scanID, Path: "src/"})
	testutil.FailErr(t, "query path", err)
	if resp.TotalMatch != 1 || len(resp.Guidance) != 1 || resp.Guidance[0].Code != "LOCATED" {
		t.Fatalf("path-filtered guidance = %#v", resp)
	}
}

func TestScanQueryDoesNotRunScanner(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := scan.NewSQLStore(sqlDB)
	mock := &scan.MockScanner{}
	reg := &scan.MockRegistry{Scanner: mock}
	coord := newTestCoordinator(t, store, nil)
	scanID := seedScanWithGuidance(t, store, "", []api.ScanGuidanceSummary{
		{Code: "SCAN_FINDING_UNMAPPED", Message: "x", RuleID: "r1"},
	})

	_, err := coord.Query(context.Background(), scan.QueryRequest{ScanID: scanID})
	testutil.FailErr(t, "coord.Query failed", err)
	if mock.RunCalls != 0 {
		t.Fatalf("RunBest invoked %d times", mock.RunCalls)
	}
	_ = reg
}
