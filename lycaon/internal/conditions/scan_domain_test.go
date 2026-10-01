package conditions_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type staticSourceSnapshot struct {
	id  string
	err error
}

func (s staticSourceSnapshot) SourceSnapshotID(context.Context, string) (string, error) {
	return s.id, s.err
}

type stubScanLedger struct {
	inflight map[string]*api.CodeScan
	complete map[string]*api.CodeScan
	latest   *api.CodeScan
}

func (s stubScanLedger) LatestRequiredScanForDelegation(context.Context, string) (*api.CodeScan, error) {
	return s.latest, nil
}

func scanKey(delegationID, snapshotID string, categories []api.ScanCategory) string {
	cats := ""
	for _, c := range categories {
		cats += string(c) + ","
	}
	return delegationID + "/" + snapshotID + "/" + cats
}

func completeSecurityArtifacts(snapshotID string) map[string]any {
	return map[string]any{
		"head_sha":              snapshotID,
		"source_snapshot_id":    snapshotID,
		"sarif_statistics":      map[string]any{"total": 0},
		"coverage_status":       "complete",
		"execution_fingerprint": "test-fingerprint",
		"fingerprint_scheme":    "test-v1",
	}
}

func (s stubScanLedger) FindInFlightForDelegationSnapshot(_ context.Context, delegationID, snapshotID string, categories []api.ScanCategory, _ []string) (*api.CodeScan, error) {
	if s.inflight == nil {
		return nil, nil
	}
	return s.inflight[scanKey(delegationID, snapshotID, categories)], nil
}

func (s stubScanLedger) LatestCompleteForDelegationSnapshot(_ context.Context, delegationID, snapshotID string, categories []api.ScanCategory) (*api.CodeScan, error) {
	if s.complete == nil {
		return nil, nil
	}
	return s.complete[scanKey(delegationID, snapshotID, categories)], nil
}

func scanDomainDeps(ledger stubScanLedger, ev stubEvidence) conditions.RegistryDeps {
	cats := []api.ScanCategory{api.ScanCategorySecurity, api.ScanCategorySecret}
	return conditions.RegistryDeps{
		DelegationStore: stubDelegationStore{
			"dep-1": {id: "dep-1", legs: []api.Leg{{ID: "leg-1", Status: api.LegStatusComplete}}},
		},
		Evidence:                ev,
		ScanLedger:              ledger,
		SourceSnapshots:         staticSourceSnapshot{id: "head-1"},
		ScanProactiveCategories: cats,
		DelegationCloseout: func(context.Context, string) (bool, error) {
			return true, nil
		},
	}
}

func TestScanPendingAndStale(t *testing.T) {
	ledger := stubScanLedger{
		latest: &api.CodeScan{ID: "scan-run", Status: api.CodeScanStatusRunning},
	}
	reg, err := conditions.NewDefaultRegistry(scanDomainDeps(ledger, nil))
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{Ctx: context.Background(), SessionID: "sess-1", ProjectDir: "/tmp/p"}

	ok, err := reg.Evaluate("scan_pending", ec)
	if err != nil || !ok {
		t.Fatalf("scan_pending = %v err=%v", ok, err)
	}
	ok, err = reg.Evaluate("scan_stale", ec)
	if err != nil || ok {
		t.Fatalf("scan_stale while pending = %v err=%v want false", ok, err)
	}

	ledger.latest = nil
	reg, err = conditions.NewDefaultRegistry(scanDomainDeps(ledger, nil))
	testutil.FailErr(t, "build conditions registry", err)
	ok, err = reg.Evaluate("scan_pending", ec)
	if err != nil || ok {
		t.Fatalf("scan_pending without inflight = %v err=%v", ok, err)
	}
	ok, err = reg.Evaluate("scan_stale", ec)
	if err != nil || ok {
		t.Fatalf("scan_stale without obligation = %v err=%v want false", ok, err)
	}
}

func TestRequiredScansEnqueued(t *testing.T) {
	ledger := stubScanLedger{
		latest: &api.CodeScan{ID: "scan-done", Status: api.CodeScanStatusComplete},
	}
	reg, err := conditions.NewDefaultRegistry(scanDomainDeps(ledger, nil))
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{Ctx: context.Background(), SessionID: "sess-1", ProjectDir: "/tmp/p"}

	ok, err := reg.Evaluate("required_scans_enqueued", ec)
	if err != nil || !ok {
		t.Fatalf("required_scans_enqueued = %v err=%v", ok, err)
	}

	regEmpty, err := conditions.NewDefaultRegistry(scanDomainDeps(stubScanLedger{}, nil))
	testutil.FailErr(t, "build conditions registry", err)
	ok, err = regEmpty.Evaluate("required_scans_enqueued", ec)
	if err != nil || !ok {
		t.Fatalf("required_scans_enqueued empty = %v err=%v want true", ok, err)
	}
}

func TestLintGatePassed(t *testing.T) {
	lintKey := scanKey("dep-1", "head-1", []api.ScanCategory{api.ScanCategoryLint})
	ledger := stubScanLedger{
		complete: map[string]*api.CodeScan{
			lintKey: {
				ID:     "lint-1",
				Status: api.CodeScanStatusComplete,
				Guidance: []api.ScanGuidanceSummary{{
					Severity: "warning",
				}},
			},
		},
	}
	reg, err := conditions.NewDefaultRegistry(scanDomainDeps(ledger, nil))
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{Ctx: context.Background(), SessionID: "sess-1", ProjectDir: "/tmp/p"}
	ok, err := reg.Evaluate("lint_gate_passed", ec)
	if err != nil || !ok {
		t.Fatalf("lint_gate_passed = %v err=%v", ok, err)
	}

	ledger.complete[lintKey].Guidance = []api.ScanGuidanceSummary{{Severity: "error"}}
	ok, err = reg.Evaluate("lint_gate_passed", ec)
	if err != nil || ok {
		t.Fatalf("lint_gate_passed with error guidance = %v err=%v want false", ok, err)
	}
}

func TestToolIsScanPack(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err := reg.Evaluate("tool_is_scan_pack", conditions.EvalContext{ToolName: "scan_pack"})
	if err != nil || !ok {
		t.Fatalf("tool_is_scan_pack = %v err=%v", ok, err)
	}
	ok, err = reg.Evaluate("tool_is_scan_pack", conditions.EvalContext{ToolName: "scan_query"})
	if err != nil || ok {
		t.Fatalf("tool_is_scan_pack must not match drill-down tools: ok=%v err=%v", ok, err)
	}
}

func TestEvidencePassedSecurityUsesScanLedger(t *testing.T) {
	ledger := stubScanLedger{
		latest: &api.CodeScan{ID: "sec-1", Status: api.CodeScanStatusComplete, SourceSnapshotID: "head-1"},
	}
	ev := stubEvidence{
		"dep-1/scan:sec-1/security": {
			GateType:    string(evidence.GateTypeSecurity),
			GateVerdict: string(evidence.GateVerdictPassed),
			Artifacts:   completeSecurityArtifacts("head-1"),
		},
	}
	reg, err := conditions.NewDefaultRegistry(scanDomainDeps(ledger, ev))
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{Ctx: context.Background(), SessionID: "sess-1", ProjectDir: "/tmp/p"}

	ok, err := reg.Evaluate("evidence_passed:security", ec)
	if err != nil || !ok {
		t.Fatalf("evidence_passed:security = %v err=%v", ok, err)
	}
	ok, err = reg.Evaluate("evidence_missing:security", ec)
	if err != nil || ok {
		t.Fatalf("evidence_missing:security = %v err=%v want false", ok, err)
	}

	ev["dep-1/scan:sec-1/security"] = &evidence.Record{
		GateType:    string(evidence.GateTypeSecurity),
		GateVerdict: string(evidence.GateVerdictPassed),
		Artifacts:   completeSecurityArtifacts("stale-head"),
	}
	ok, err = reg.Evaluate("evidence_passed:security", ec)
	if err != nil || ok {
		t.Fatalf("stale head security = %v err=%v want false", ok, err)
	}
}
