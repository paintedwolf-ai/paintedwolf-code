package conditions_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/testutil"
)

type stubEvidence map[string]*evidence.Record

func (s stubEvidence) LatestEvidence(_ context.Context, _, runID, taskID string, gateType evidence.GateType, scope inspector.EvidenceScope) (*evidence.Record, error) {
	key := runID + "/" + taskID + "/" + string(gateType)
	rec := s[key]
	if rec == nil || scope.IsZero() {
		return rec, nil
	}
	if inspector.LatestScoped([]evidence.Record{*rec}, scope) == nil {
		return nil, nil
	}
	return rec, nil
}

func TestEvidencePassedRequiresAnchors(t *testing.T) {
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
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		SessionWorkIDs: workIDs,
		Evidence:       ev,
		DelegationCloseout: func(context.Context, string) (bool, error) {
			return true, nil
		},
	})
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{
		Ctx:       context.Background(),
		SessionID: "sess-1",
	}
	ok, err := reg.Evaluate("evidence_passed:verify", ec)
	if err != nil || !ok {
		t.Fatalf("evidence_passed:verify = %v err=%v", ok, err)
	}
	ev["sess-1/leg-1/verify"] = &evidence.Record{
		GateType:    string(evidence.GateTypeVerify),
		GateVerdict: string(evidence.GateVerdictPassed),
		Artifacts: map[string]any{
			"exit_code": 0,
		},
	}
	ok, err = reg.Evaluate("evidence_passed:verify", ec)
	if err != nil || ok {
		t.Fatalf("unanchored evidence = %v err=%v want false", ok, err)
	}
}

func TestEvidencePassedRespectsLegScope(t *testing.T) {
	ev := stubEvidence{
		"sess-1/leg-a/verify": {
			GateType:    string(evidence.GateTypeVerify),
			GateVerdict: string(evidence.GateVerdictPassed),
			Artifacts: map[string]any{
				inspector.ArtifactLegID: "leg-a",
				"exit_code":             0,
				"command":               "go test ./...",
			},
		},
		"sess-1/leg-b/verify": {
			GateType:    string(evidence.GateTypeVerify),
			GateVerdict: string(evidence.GateVerdictPassed),
			Artifacts: map[string]any{
				inspector.ArtifactLegID: "leg-b",
				"exit_code":             0,
				"command":               "go test ./...",
			},
		},
	}
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		SessionWorkIDs: func(context.Context, string) ([]string, error) {
			return []string{"leg-a", "leg-b"}, nil
		},
		Evidence: ev,
	})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err := reg.Evaluate("evidence_passed:verify", conditions.EvalContext{
		Ctx:           context.Background(),
		SessionID:     "sess-1",
		EvidenceLegID: "leg-a",
	})
	if err != nil || !ok {
		t.Fatalf("leg-a verify = %v err=%v", ok, err)
	}
	ok, err = reg.Evaluate("evidence_passed:verify", conditions.EvalContext{
		Ctx:           context.Background(),
		SessionID:     "sess-1",
		EvidenceLegID: "leg-missing",
	})
	if err != nil || ok {
		t.Fatalf("missing leg scope = %v err=%v want false", ok, err)
	}
}
