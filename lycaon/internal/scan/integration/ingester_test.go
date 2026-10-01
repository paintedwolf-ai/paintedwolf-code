package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestIngesterEvidenceAnchorsCleanScan(t *testing.T) {
	ing := &scan.IngesterImpl{
		Module:  scancfg.DefaultModuleConfig(),
		Budget:  scancfg.NewFindingBudget(scancfg.DefaultAgentBudget()),
		BlockOn: []string{"error"},
	}
	rec, err := ing.Ingest(context.Background(), scan.ScanSourceRegistry, &scanoutput.Result{
		FindingsCount: 0,
	}, completeIngestMeta(t, scan.IngestMeta{
		ScanID:           "scan-1",
		ProjectDir:       t.TempDir(),
		HeadSHA:          "abc123",
		SourceSnapshotID: "snapshot-1",
		Scanner:          testScannerContract("test-security"),
		Categories:       []api.ScanCategory{api.ScanCategorySecurity},
	}))
	testutil.FailErr(t, "ing.Ingest failed", err)
	if rec.TypedGateType() != evidence.GateTypeSecurity {
		t.Fatalf("type = %q", rec.TypedGateType())
	}
	if rec.HeadSHA != "abc123" {
		t.Fatalf("head_sha = %q", rec.HeadSHA)
	}
	ok, reason := inspector.EvidenceAnchored(rec)
	if !ok {
		t.Fatalf("expected anchored evidence: %s", reason)
	}
	stats, _ := rec.Artifacts["sarif_statistics"].(map[string]any)
	if stats == nil {
		t.Fatal("missing sarif_statistics")
	}
	if _, ok := rec.Artifacts["engine_proof"]; !ok {
		t.Fatal("missing engine_proof")
	}
	if _, ok := rec.Artifacts["finding_count"]; !ok {
		t.Fatal("missing finding_count")
	}
}

func TestIngesterEvidenceBlocksOnErrorSeverity(t *testing.T) {
	ing := &scan.IngesterImpl{
		Module:  scancfg.DefaultModuleConfig(),
		Budget:  scancfg.NewFindingBudget(scancfg.DefaultAgentBudget()),
		BlockOn: []string{"error"},
	}
	rec, err := ing.Ingest(context.Background(), scan.ScanSourceRegistry, &scanoutput.Result{
		FindingsCount: 1,
		Findings: []api.SecurityFinding{
			scanfindings.FixtureFinding("lycaon.ruby.sql-string-concat", api.FindingLevelHigh, "sql concat", "app.rb", 10),
		},
	}, completeIngestMeta(t, scan.IngestMeta{
		ScanID:           "scan-err",
		ProjectDir:       t.TempDir(),
		HeadSHA:          "abc123",
		SourceSnapshotID: "snapshot-err",
		Scanner:          testScannerContract("test-security"),
		Categories:       []api.ScanCategory{api.ScanCategorySecurity},
	}))
	testutil.FailErr(t, "ing.Ingest failed", err)
	if rec.TypedGateVerdict() != evidence.GateVerdictFailed {
		t.Fatalf("verdict = %q", rec.TypedGateVerdict())
	}
	ok, reason := inspector.EvidenceAnchored(rec)
	if ok {
		t.Fatalf("failed verdict must not satisfy gate: %s", reason)
	}
	guidance, _ := rec.Artifacts["guidance"].([]api.ScanGuidanceSummary)
	if len(guidance) != 1 || guidance[0].Code != "SCAN_SQL_INJECTION" {
		t.Fatalf("guidance = %#v", guidance)
	}
}

func TestIngesterStoresFullGuidanceNotInjectionCap(t *testing.T) {
	ing := &scan.IngesterImpl{
		Module: scancfg.DefaultModuleConfig(),
		Budget: scancfg.NewFindingBudget(scancfg.AgentBudgetConfig{
			MaxGuidanceFindings:  500,
			MaxHintsPerInjection: 8,
			MaxQueryResults:      20,
			MinSeverity:          "warning",
			DedupeBy:             "rule_id+path",
		}),
	}
	findings := make([]api.SecurityFinding, 0, 12)
	for i := 0; i < 12; i++ {
		findings = append(findings, scanfindings.FixtureFinding(
			"lycaon.ruby.sql-string-concat",
			api.FindingLevelHigh,
			"sql concat",
			fmt.Sprintf("app_%d.rb", i),
			1,
		))
	}
	rec, err := ing.Ingest(context.Background(), scan.ScanSourceRegistry, &scanoutput.Result{
		FindingsCount: len(findings),
		Findings:      findings,
	}, completeIngestMeta(t, scan.IngestMeta{
		ScanID:           "scan-many",
		ProjectDir:       t.TempDir(),
		HeadSHA:          "abc123",
		SourceSnapshotID: "snapshot-many",
		Scanner:          testScannerContract("test-security"),
		Categories:       []api.ScanCategory{api.ScanCategorySecurity},
	}))
	testutil.FailErr(t, "ing.Ingest failed", err)
	guidance, _ := rec.Artifacts["guidance"].([]api.ScanGuidanceSummary)
	if len(guidance) <= scancfg.DefaultAgentBudget().MaxHintsPerInjection {
		t.Fatalf("guidance len = %d want > injection cap %d", len(guidance), scancfg.DefaultAgentBudget().MaxHintsPerInjection)
	}
}
