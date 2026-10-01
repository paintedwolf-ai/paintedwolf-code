package conditions_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/evidence"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubDelegation struct {
	id   string
	legs []api.Leg
}

type stubDelegationStore map[string]stubDelegation

func (s stubDelegationStore) DelegationBySessionID(sessionID string) (string, bool) {
	for id, d := range s {
		_ = sessionID
		if id != "" {
			return id, true
		}
		_ = d
	}
	if len(s) == 0 {
		return "", false
	}
	for id := range s {
		return id, true
	}
	return "", false
}

func (s stubDelegationStore) Get(_ context.Context, delegationID string) (*api.Delegation, error) {
	d, ok := s[delegationID]
	if !ok {
		return nil, nil
	}
	return &api.Delegation{ID: delegationID, Legs: d.legs}, nil
}

func (s stubDelegationStore) ListLegs(_ context.Context, delegationID string) ([]api.Leg, error) {
	d, ok := s[delegationID]
	if !ok {
		return nil, nil
	}
	return d.legs, nil
}

type stubEvidence map[string]*evidence.Record

func (s stubEvidence) LatestEvidence(_ context.Context, _, delegationID, taskID string, gateType evidence.GateType, scope inspector.EvidenceScope) (*evidence.Record, error) {
	key := delegationID + "/" + taskID + "/" + string(gateType)
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
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		DelegationStore: depStore,
		Evidence:        ev,
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
	ev["dep-1/leg-1/verify"] = &evidence.Record{
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
	depStore := stubDelegationStore{"dep-1": {id: "dep-1", legs: []api.Leg{
		{ID: "leg-a", Status: api.LegStatusComplete},
		{ID: "leg-b", Status: api.LegStatusComplete},
	}}}
	ev := stubEvidence{
		"dep-1/leg-a/verify": {
			GateType:    string(evidence.GateTypeVerify),
			GateVerdict: string(evidence.GateVerdictPassed),
			Artifacts: map[string]any{
				inspector.ArtifactLegID: "leg-a",
				"exit_code":             0,
				"command":               "go test ./...",
			},
		},
		"dep-1/leg-b/verify": {
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
		DelegationStore: depStore,
		Evidence:        ev,
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
