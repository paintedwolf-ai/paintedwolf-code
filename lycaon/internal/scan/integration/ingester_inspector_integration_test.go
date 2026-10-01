//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestIngesterInspectorIntegrationRecordEvidence(t *testing.T) {
	projectDir := t.TempDir()
	store := inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	ins := inspector.NewSimpleInspector(store)
	ins.ProjectDir = func(context.Context, string) (string, error) { return projectDir, nil }

	ing := &scan.IngesterImpl{
		Inspector: ins,
		Module:    scancfg.DefaultModuleConfig(),
		Budget:    scancfg.NewFindingBudget(scancfg.DefaultAgentBudget()),
		BlockOn:   []string{"error"},
	}
	_, err := ing.Ingest(context.Background(), scan.ScanSourceRegistry, &scanoutput.Result{
		Findings: []api.SecurityFinding{
			scanfindings.FixtureFinding("gitleaks:github-pat", api.FindingLevelHigh, "", ".env", 0),
		},
	}, completeIngestMeta(t, scan.IngestMeta{
		ScanID:           "scan-1",
		ProjectDir:       projectDir,
		HeadSHA:          "deadbeef",
		SourceSnapshotID: "snapshot-1",
		DelegationID:     "dep-1",
		TaskID:           "task-1",
		Scanner:          testScannerContract("test-security"),
		Categories:       []api.ScanCategory{api.ScanCategorySecurity},
	}))
	testutil.FailErr(t, "ing.Ingest failed", err)

	records, err := store.ReadAll(context.Background(), projectDir, "dep-1", "task-1", evidence.GateTypeSecurity)
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %d err=%v", len(records), err)
	}
	ok, reason := inspector.EvidenceAnchored(records[0])
	if ok {
		t.Fatalf("error-severity evidence must not anchor for gates: %s", reason)
	}
	if records[0].TypedGateVerdict() != evidence.GateVerdictFailed {
		t.Fatalf("verdict = %q", records[0].TypedGateVerdict())
	}
}

func TestIngesterInspectorIntegrationCheckGatesReadsLatestClean(t *testing.T) {
	projectDir := t.TempDir()
	store := inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	ins := inspector.NewSimpleInspector(store)
	ins.ProjectDir = func(context.Context, string) (string, error) { return projectDir, nil }
	ins.Required = []evidence.GateType{evidence.GateTypeSecurity}

	ing := &scan.IngesterImpl{
		Inspector: ins,
		Module:    scancfg.DefaultModuleConfig(),
		Budget:    scancfg.NewFindingBudget(scancfg.DefaultAgentBudget()),
		BlockOn:   []string{"error"},
	}
	_, err := ing.Ingest(context.Background(), scan.ScanSourceRegistry, &scanoutput.Result{
		FindingsCount: 0,
	}, completeIngestMeta(t, scan.IngestMeta{
		ScanID:           "scan-2",
		ProjectDir:       projectDir,
		HeadSHA:          "cafebabe",
		SourceSnapshotID: "snapshot-2",
		DelegationID:     "dep-2",
		TaskID:           "task-2",
		Scanner:          testScannerContract("test-security"),
		Categories:       []api.ScanCategory{api.ScanCategorySecurity},
	}))
	testutil.FailErr(t, "ing.Ingest failed", err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := ins.CheckGates(ctx, "dep-2", []string{"task-2"}, nil)
	testutil.FailErr(t, "ins.CheckGates failed", err)
	if !result.OK {
		t.Fatalf("expected gates ok: missing=%v errors=%v", result.Missing, result.Errors)
	}
}
