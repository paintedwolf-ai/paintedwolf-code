package conditions_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBaselineScanCompleteWhenLedgerComplete(t *testing.T) {
	proactive := []api.ScanCategory{api.ScanCategorySecurity, api.ScanCategorySecret}
	key := scanKey("dep-1", "head-1", proactive)
	ledger := stubScanLedger{
		complete: map[string]*api.CodeScan{
			key: {ID: "sec-1", Status: api.CodeScanStatusComplete, HeadSHA: "head-1"},
		},
	}
	reg, err := conditions.NewDefaultRegistry(scanDomainDeps(ledger, nil))
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{Ctx: context.Background(), SessionID: "sess-1", ProjectDir: "/tmp/p"}

	ok, err := reg.Evaluate("baseline_scan_complete", ec)
	if err != nil || !ok {
		t.Fatalf("baseline_scan_complete = %v err=%v", ok, err)
	}

	regEmpty, err := conditions.NewDefaultRegistry(scanDomainDeps(stubScanLedger{}, nil))
	testutil.FailErr(t, "build conditions registry", err)
	ok, err = regEmpty.Evaluate("baseline_scan_complete", ec)
	if err != nil || ok {
		t.Fatalf("baseline_scan_complete empty = %v err=%v want false", ok, err)
	}
}

func TestSecurityEvidenceRequiresSnapshotMatch(t *testing.T) {
	ec := conditions.EvalContext{Ctx: context.Background(), SessionID: "sess-1", ProjectDir: "/tmp/p"}

	ledgerFresh := stubScanLedger{
		latest: &api.CodeScan{ID: "sec-1", Status: api.CodeScanStatusComplete, SourceSnapshotID: "snapshot-1"},
	}
	freshEvidence := stubEvidence{
		"dep-1/scan:sec-1/security": &evidence.Record{
			GateType: string(evidence.GateTypeSecurity), GateVerdict: string(evidence.GateVerdictPassed),
			Artifacts: completeSecurityArtifacts("snapshot-1"),
		},
	}
	reg, err := conditions.NewDefaultRegistry(scanDomainDeps(ledgerFresh, freshEvidence))
	testutil.FailErr(t, "build conditions registry", err)
	ok, err := reg.Evaluate("security_evidence_fresh", ec)
	if err != nil || !ok {
		t.Fatalf("security_evidence_fresh = %v err=%v", ok, err)
	}

	ledgerStale := stubScanLedger{
		latest: &api.CodeScan{ID: "sec-1", Status: api.CodeScanStatusComplete, SourceSnapshotID: "snapshot-1"},
	}
	staleEvidence := stubEvidence{
		"dep-1/scan:sec-1/security": &evidence.Record{
			GateType: string(evidence.GateTypeSecurity), GateVerdict: string(evidence.GateVerdictPassed),
			Artifacts: completeSecurityArtifacts("snapshot-old"),
		},
	}
	regStale, err := conditions.NewDefaultRegistry(scanDomainDeps(ledgerStale, staleEvidence))
	testutil.FailErr(t, "build conditions registry", err)
	ok, err = regStale.Evaluate("security_evidence_fresh", ec)
	if err != nil || ok {
		t.Fatalf("stale snapshot security_evidence_fresh = %v err=%v want false", ok, err)
	}

	ledgerInflight := stubScanLedger{
		latest: &api.CodeScan{ID: "run-1", Status: api.CodeScanStatusRunning, SourceSnapshotID: "snapshot-1"},
	}
	regInflight, err := conditions.NewDefaultRegistry(scanDomainDeps(ledgerInflight, nil))
	testutil.FailErr(t, "build conditions registry", err)
	ok, err = regInflight.Evaluate("security_evidence_fresh", ec)
	if err != nil || ok {
		t.Fatalf("inflight security_evidence_fresh = %v err=%v want false", ok, err)
	}
}

func TestNoCriticalFindingsWhenSummaryClean(t *testing.T) {
	ec := conditions.EvalContext{Ctx: context.Background(), SessionID: "sess-1", ProjectDir: "/tmp/p"}

	clean := stubScanLedger{
		latest: &api.CodeScan{
			ID:     "sec-clean",
			Status: api.CodeScanStatusComplete,
			FindingsByLevel: map[string]int{
				string(api.FindingLevelMedium): 1,
			},
		},
	}
	reg, err := conditions.NewDefaultRegistry(scanDomainDeps(clean, nil))
	testutil.FailErr(t, "build conditions registry", err)
	ok, err := reg.Evaluate("no_critical_findings", ec)
	if err != nil || !ok {
		t.Fatalf("no_critical_findings clean = %v err=%v", ok, err)
	}

	critical := stubScanLedger{
		latest: &api.CodeScan{
			ID:     "sec-bad",
			Status: api.CodeScanStatusComplete,
			Findings: []api.SecurityFinding{
				{Level: api.FindingLevelCritical, RuleID: "r1"},
			},
			FindingsByLevel: map[string]int{
				string(api.FindingLevelCritical): 1,
			},
		},
	}
	regBad, err := conditions.NewDefaultRegistry(scanDomainDeps(critical, nil))
	testutil.FailErr(t, "build conditions registry", err)
	ok, err = regBad.Evaluate("no_critical_findings", ec)
	if err != nil || ok {
		t.Fatalf("no_critical_findings critical = %v err=%v want false", ok, err)
	}
}

func TestShippedScanPredicatesNotAlwaysFalse(t *testing.T) {
	proactive := []api.ScanCategory{api.ScanCategorySecurity, api.ScanCategorySecret}
	key := scanKey("dep-1", "head-1", proactive)
	ledger := stubScanLedger{
		complete: map[string]*api.CodeScan{
			key: {
				ID:       "sec-1",
				Status:   api.CodeScanStatusComplete,
				HeadSHA:  "head-1",
				Guidance: []api.ScanGuidanceSummary{{Severity: "info"}},
			},
		},
		latest: &api.CodeScan{
			ID: "sec-1", Status: api.CodeScanStatusComplete, SourceSnapshotID: "head-1",
		},
	}
	ev := stubEvidence{
		"dep-1/scan:sec-1/security": &evidence.Record{
			GateType: string(evidence.GateTypeSecurity), GateVerdict: string(evidence.GateVerdictPassed),
			Artifacts: completeSecurityArtifacts("head-1"),
		},
	}
	reg, err := conditions.NewDefaultRegistry(scanDomainDeps(ledger, ev))
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{Ctx: context.Background(), SessionID: "sess-1", ProjectDir: "/tmp/p"}

	for _, id := range []string{
		"baseline_scan_complete",
		"security_evidence_fresh",
		"no_critical_findings",
	} {
		ok, err := reg.Evaluate(id, ec)
		if err != nil || !ok {
			t.Fatalf("%s with satisfied ledger = %v err=%v want true", id, ok, err)
		}
	}
}

func TestDeferredScanCatalogStubIDsDocumented(t *testing.T) {
	want := map[string]bool{
		"findings_triaged":     true,
		"remediation_complete": true,
	}
	for _, id := range conditions.ScanCatalogStubIDs() {
		if !want[id] {
			t.Fatalf("unexpected deferred scan catalog stub %q", id)
		}
		delete(want, id)
	}
	if len(want) > 0 {
		t.Fatalf("missing deferred scan catalog stubs: %v", want)
	}
	for _, id := range conditions.ShippedScanDomainIDs() {
		if id == "findings_triaged" || id == "remediation_complete" {
			t.Fatalf("deferred id %q must not be shipped", id)
		}
	}
}

func TestDeferredScanCatalogStubsNotRegistered(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	for _, id := range conditions.ScanCatalogStubIDs() {
		if reg.Has(id) {
			t.Fatalf("deferred catalog stub %q must not register always-false evaluator", id)
		}
	}
}
