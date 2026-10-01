package conditions_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDelegationCloseoutCompleteInCore(t *testing.T) {
	coreReg := conditions.NewRegistry()
	if err := conditions.RegisterCoreConditions(coreReg, conditions.CoreDeps{}); err != nil {
		testutil.FailErr(t, "conditions.RegisterCoreConditions failed", err)
	}
	if !coreReg.Has("delegation_closeout_complete") {
		t.Fatal("delegation_closeout_complete must be core")
	}
	for _, id := range conditions.ShippedPlanDomainIDs() {
		if id == "delegation_closeout_complete" {
			t.Fatal("delegation_closeout_complete must not remain in plan domain")
		}
	}
}

func TestCloseoutGatesPassedRequiresDelegationAndVerify(t *testing.T) {
	depStore := stubDelegationStore{"dep-1": {id: "dep-1", legs: []api.Leg{{ID: "leg-1", Status: api.LegStatusComplete}}}}
	ev := stubEvidence{
		"dep-1/leg-1/verify": {
			GateType:    string(evidence.GateTypeVerify),
			GateVerdict: string(evidence.GateVerdictPassed),
			Artifacts: map[string]any{
				"exit_code": 0,
				"command":   "go test ./...",
			},
		},
	}
	closeout := func(_ context.Context, _ string) (bool, error) { return true, nil }
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		DelegationStore:    depStore,
		Evidence:           ev,
		DelegationCloseout: closeout,
	})
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{Ctx: context.Background(), SessionID: "sess-1"}

	ok, err := reg.Evaluate("closeout_gates_passed", ec)
	if err != nil || !ok {
		t.Fatalf("closeout_gates_passed = %v err=%v", ok, err)
	}

	regNoVerify, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		DelegationStore:    depStore,
		Evidence:           stubEvidence{},
		DelegationCloseout: closeout,
	})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err = regNoVerify.Evaluate("closeout_gates_passed", ec)
	if err != nil || ok {
		t.Fatalf("closeout without verify = %v err=%v want false", ok, err)
	}

	pendingLegs := stubDelegationStore{"dep-2": {id: "dep-2", legs: []api.Leg{{ID: "leg-1", Status: api.LegStatusRunning}}}}
	pendingCloseout := func(_ context.Context, _ string) (bool, error) { return false, nil }
	regPending, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		DelegationStore:    pendingLegs,
		Evidence:           ev,
		DelegationCloseout: pendingCloseout,
	})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err = regPending.Evaluate("closeout_gates_passed", ec)
	if err != nil || ok {
		t.Fatalf("closeout with active delegation = %v err=%v want false", ok, err)
	}
}

func TestCloseoutGatesPassedAcceptsCurrentSourceValidation(t *testing.T) {
	depStore := stubDelegationStore{"dep-1": {id: "dep-1", legs: []api.Leg{{ID: "leg-1", Status: api.LegStatusComplete}}}}
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		DelegationStore:    depStore,
		Evidence:           stubEvidence{},
		DelegationCloseout: func(context.Context, string) (bool, error) { return true, nil },
		SourceVerifyPassed: func(_ context.Context, sessionID string) (bool, error) {
			return sessionID == "sess-1", nil
		},
	})
	testutil.FailErr(t, "build conditions registry", err)

	ok, err := reg.Evaluate("closeout_gates_passed", conditions.EvalContext{Ctx: t.Context(), SessionID: "sess-1"})
	if err != nil || !ok {
		t.Fatalf("closeout with current source validation = %v err=%v", ok, err)
	}
}

func TestCloseoutGatesPassedRequiresSecurityEvidence(t *testing.T) {
	proactive := []api.ScanCategory{api.ScanCategorySecurity, api.ScanCategorySecret}
	depStore := stubDelegationStore{"dep-1": {id: "dep-1", legs: []api.Leg{{ID: "leg-1", Status: api.LegStatusComplete}}}}
	verifyEv := stubEvidence{
		"dep-1/leg-1/verify": {
			GateType:    string(evidence.GateTypeVerify),
			GateVerdict: string(evidence.GateVerdictPassed),
			Artifacts: map[string]any{
				"exit_code": 0,
				"command":   "go test ./...",
			},
		},
	}
	closeout := func(_ context.Context, _ string) (bool, error) { return true, nil }
	ledger := stubScanLedger{latest: &api.CodeScan{
		ID: "sec-1", Status: api.CodeScanStatusComplete, SourceSnapshotID: "head-1",
	}}
	baseDeps := conditions.RegistryDeps{
		DelegationStore:         depStore,
		Evidence:                verifyEv,
		DelegationCloseout:      closeout,
		ScanLedger:              ledger,
		SourceSnapshots:         staticSourceSnapshot{id: "head-1"},
		ScanProactiveCategories: proactive,
	}
	regNoSecurity, err := conditions.NewDefaultRegistry(baseDeps)
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{Ctx: context.Background(), SessionID: "sess-1", ProjectDir: "/tmp/p"}
	ok, err := regNoSecurity.Evaluate("closeout_gates_passed", ec)
	if err != nil || ok {
		t.Fatalf("closeout without security evidence = %v err=%v want false", ok, err)
	}

	secEv := stubEvidence{
		"dep-1/leg-1/verify": verifyEv["dep-1/leg-1/verify"],
		"dep-1/scan:sec-1/security": {
			GateType:    string(evidence.GateTypeSecurity),
			GateVerdict: string(evidence.GateVerdictPassed),
			Artifacts:   completeSecurityArtifacts("head-1"),
		},
	}
	regPass, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		DelegationStore:         depStore,
		Evidence:                secEv,
		DelegationCloseout:      closeout,
		ScanLedger:              ledger,
		SourceSnapshots:         staticSourceSnapshot{id: "head-1"},
		ScanProactiveCategories: proactive,
	})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err = regPass.Evaluate("closeout_gates_passed", ec)
	if err != nil || !ok {
		t.Fatalf("closeout with anchored security = %v err=%v", ok, err)
	}
	ok, err = regPass.Evaluate("evidence_passed:security", ec)
	if err != nil || !ok {
		t.Fatalf("evidence_passed:security = %v err=%v", ok, err)
	}
}

func TestCloseoutGatesPassedUsesCumulativeLandingObligation(t *testing.T) {
	depStore := stubDelegationStore{"dep-1": {id: "dep-1", legs: []api.Leg{{ID: "leg-1", Status: api.LegStatusComplete}}}}
	evidenceRows := stubEvidence{
		"dep-1/leg-1/verify": {
			GateType: string(evidence.GateTypeVerify), GateVerdict: string(evidence.GateVerdictPassed),
			Artifacts: map[string]any{"exit_code": 0, "command": "./task check"},
		},
		"dep-1/scan:landing-scan/security": {
			GateType: string(evidence.GateTypeSecurity), GateVerdict: string(evidence.GateVerdictPassed),
			Artifacts: completeSecurityArtifacts("landing-snapshot"),
		},
	}
	ledger := stubScanLedger{latest: &api.CodeScan{
		ID: "landing-scan", Status: api.CodeScanStatusComplete, SourceSnapshotID: "landing-snapshot",
	}}
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		DelegationStore: depStore, Evidence: evidenceRows, ScanLedger: ledger,
		DelegationCloseout: func(context.Context, string) (bool, error) { return true, nil },
		SourceSnapshots:    staticSourceSnapshot{id: "unrelated-snapshot"},
	})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err := reg.Evaluate("closeout_gates_passed", conditions.EvalContext{
		Ctx: t.Context(), SessionID: "sess-1", ProjectDir: "/tmp/p",
	})
	if err != nil || !ok {
		t.Fatalf("cumulative landing closeout = %v err=%v", ok, err)
	}
}

func TestCloseoutGatesPassedHonorsScannerMasterOff(t *testing.T) {
	depStore := stubDelegationStore{"dep-1": {id: "dep-1", legs: []api.Leg{{ID: "leg-1", Status: api.LegStatusComplete}}}}
	verify := stubEvidence{
		"dep-1/leg-1/verify": {
			GateType: string(evidence.GateTypeVerify), GateVerdict: string(evidence.GateVerdictPassed),
			Artifacts: map[string]any{"exit_code": 0, "command": "./task check"},
		},
	}
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		DelegationStore: depStore, Evidence: verify, ScanLedger: stubScanLedger{},
		DelegationCloseout:      func(context.Context, string) (bool, error) { return true, nil },
		SecurityScannersEnabled: func() bool { return false },
	})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err := reg.Evaluate("closeout_gates_passed", conditions.EvalContext{Ctx: t.Context(), SessionID: "sess-1"})
	if err != nil || !ok {
		t.Fatalf("master-off closeout = %v err=%v", ok, err)
	}
}

// stubEvidence satisfies EvidenceReader.
var _ interface {
	LatestEvidence(context.Context, string, string, string, evidence.GateType, inspector.EvidenceScope) (*evidence.Record, error)
} = stubEvidence(nil)
