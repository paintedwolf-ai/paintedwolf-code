package conditions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestExplicitCheckGateUsesCurrentSource(t *testing.T) {
	for _, condition := range []string{"evidence_passed:verify", "evidence_passed:test", "closeout_gates_passed"} {
		t.Run(condition, func(t *testing.T) {
			passed := false
			var failure error
			deps := conditions.RegistryDeps{
				DelegationStore:    stubDelegationStore{"dep": {id: "dep", legs: []api.Leg{{ID: "leg", Status: api.LegStatusComplete}}}},
				Evidence:           stubEvidence{"dep/leg/verify": &evidence.Record{GateType: "verify", GateVerdict: "passed", Artifacts: map[string]any{"exit_code": 0, "command": "old-check"}}},
				DelegationCloseout: func(context.Context, string) (bool, error) { return true, nil },
				SourceVerifyPassed: func(context.Context, string) (bool, error) { return passed, failure },
			}
			reg, err := conditions.NewDefaultRegistry(deps)
			testutil.FailErr(t, "create registry", err)
			ec := conditions.EvalContext{Ctx: t.Context(), SessionID: "session"}
			got, err := reg.Evaluate(condition, ec)
			if err != nil || got {
				t.Fatalf("old leg pass bypassed current source: %v, %v", got, err)
			}
			passed = true
			got, err = reg.Evaluate(condition, ec)
			if err != nil || !got {
				t.Fatalf("current source pass rejected: %v, %v", got, err)
			}
			failure = errors.New("evidence unavailable")
			passed = false
			got, err = reg.Evaluate(condition, ec)
			if got || !errors.Is(err, failure) {
				t.Fatalf("evidence failure hidden: %v, %v", got, err)
			}
		})
	}
}

func TestDirectCheckGateNeedsNoDelegation(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		SourceVerifyPassed: func(context.Context, string) (bool, error) { return true, nil },
	})
	testutil.FailErr(t, "create registry", err)
	for _, id := range []string{"evidence_passed:verify", "evidence_passed:test"} {
		got, err := reg.Evaluate(id, conditions.EvalContext{Ctx: t.Context(), SessionID: "session"})
		if err != nil || !got {
			t.Fatalf("%s = %v, %v", id, got, err)
		}
	}
	got, err := reg.Evaluate("evidence_passed:verify", conditions.EvalContext{Ctx: t.Context(), SessionID: "session", EvidenceLegID: "other-leg"})
	if err != nil || got {
		t.Fatalf("session pass substituted for scoped leg: %v, %v", got, err)
	}
}
