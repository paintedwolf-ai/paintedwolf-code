package conditions_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/testutil/conditionstest"
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
	for _, id := range conditionstest.PlanIDs {
		if id == "delegation_closeout_complete" {
			t.Fatal("delegation_closeout_complete must not remain in plan domain")
		}
	}
}

func TestCloseoutGatesPassedRequiresWorkerCloseoutAndVerify(t *testing.T) {
	workIDs := func(context.Context, string) ([]string, error) {
		return []string{"leg-1"}, nil
	}
	ev := stubEvidence{
		"sess-1/leg-1/verify": {
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
		SessionWorkIDs: workIDs,
		Evidence:       ev,
		PlanCloseout:   closeout,
	})
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{Ctx: context.Background(), SessionID: "sess-1"}

	ok, err := reg.Evaluate("closeout_gates_passed", ec)
	if err != nil || !ok {
		t.Fatalf("closeout_gates_passed = %v err=%v", ok, err)
	}

	regNoVerify, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		SessionWorkIDs: workIDs,
		Evidence:       stubEvidence{},
		PlanCloseout:   closeout,
	})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err = regNoVerify.Evaluate("closeout_gates_passed", ec)
	if err != nil || ok {
		t.Fatalf("closeout without verify = %v err=%v want false", ok, err)
	}

	pendingCloseout := func(_ context.Context, _ string) (bool, error) { return false, nil }
	regPending, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		SessionWorkIDs: workIDs,
		Evidence:       ev,
		PlanCloseout:   pendingCloseout,
	})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err = regPending.Evaluate("closeout_gates_passed", ec)
	if err != nil || ok {
		t.Fatalf("closeout with unfinished workers = %v err=%v want false", ok, err)
	}
}

func TestCloseoutGatesPassedAcceptsCurrentSourceValidation(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		SessionWorkIDs: func(context.Context, string) ([]string, error) { return []string{"leg-1"}, nil },
		Evidence:       stubEvidence{},
		PlanCloseout:   func(context.Context, string) (bool, error) { return true, nil },
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
	workIDs := func(context.Context, string) ([]string, error) { return []string{"leg-1"}, nil }
	verifyEv := stubEvidence{
		"sess-1/leg-1/verify": {
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
		SessionWorkIDs:          workIDs,
		Evidence:                verifyEv,
		PlanCloseout:            closeout,
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
		"sess-1/leg-1/verify": verifyEv["sess-1/leg-1/verify"],
		"sess-1/scan:sec-1/security": {
			GateType:    string(evidence.GateTypeSecurity),
			GateVerdict: string(evidence.GateVerdictPassed),
			Artifacts:   completeSecurityArtifacts("head-1"),
		},
	}
	regPass, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		SessionWorkIDs:          workIDs,
		Evidence:                secEv,
		PlanCloseout:            closeout,
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
	workIDs := func(context.Context, string) ([]string, error) { return []string{"leg-1"}, nil }
	evidenceRows := stubEvidence{
		"sess-1/leg-1/verify": {
			GateType: string(evidence.GateTypeVerify), GateVerdict: string(evidence.GateVerdictPassed),
			Artifacts: map[string]any{"exit_code": 0, "command": "./task check"},
		},
		"sess-1/scan:landing-scan/security": {
			GateType: string(evidence.GateTypeSecurity), GateVerdict: string(evidence.GateVerdictPassed),
			Artifacts: completeSecurityArtifacts("landing-snapshot"),
		},
	}
	ledger := stubScanLedger{latest: &api.CodeScan{
		ID: "landing-scan", Status: api.CodeScanStatusComplete, SourceSnapshotID: "landing-snapshot",
	}}
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		SessionWorkIDs: workIDs, Evidence: evidenceRows, ScanLedger: ledger,
		PlanCloseout:    func(context.Context, string) (bool, error) { return true, nil },
		SourceSnapshots: staticSourceSnapshot{id: "unrelated-snapshot"},
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
	workIDs := func(context.Context, string) ([]string, error) { return []string{"leg-1"}, nil }
	verify := stubEvidence{
		"sess-1/leg-1/verify": {
			GateType: string(evidence.GateTypeVerify), GateVerdict: string(evidence.GateVerdictPassed),
			Artifacts: map[string]any{"exit_code": 0, "command": "./task check"},
		},
	}
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		SessionWorkIDs: workIDs, Evidence: verify, ScanLedger: stubScanLedger{},
		PlanCloseout:            func(context.Context, string) (bool, error) { return true, nil },
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
