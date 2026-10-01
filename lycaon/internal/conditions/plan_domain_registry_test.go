package conditions_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestResearchSatisfiedWhenResearchPhaseSkipped(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	vars := workflow.SetHostVar(nil, "phase_skipped.research", true)
	ok, err := reg.Evaluate("research_satisfied", conditions.EvalContext{Vars: vars})
	if err != nil || !ok {
		t.Fatalf("research_satisfied aligned with skip = %v err=%v", ok, err)
	}
}

func TestPlanDomainPredicatesEvaluateState(t *testing.T) {
	deps := conditions.RegistryDeps{
		BlueprintGet: func(_ context.Context, _ string) (*api.Blueprint, error) {
			return &api.Blueprint{Content: conditions.TestPlanContentFullSections}, nil
		},
	}
	reg, err := conditions.NewDefaultRegistry(deps)
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{BlueprintPath: "p1"}

	cases := []struct {
		id   string
		want bool
	}{
		{"scope_missing", false},
		{"breaking_missing", false},
		{"review_depth_missing", false},
	}
	for _, tc := range cases {
		ok, err := reg.Evaluate(tc.id, ec)
		if err != nil {
			t.Fatalf("%s err=%v", tc.id, err)
		}
		if ok != tc.want {
			t.Fatalf("%s = %v want %v", tc.id, ok, tc.want)
		}
	}

	incompleteStub := "---\nresearch_depth: none\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n"
	incompleteDeps := conditions.RegistryDeps{
		BlueprintGet: func(_ context.Context, _ string) (*api.Blueprint, error) {
			return &api.Blueprint{Content: incompleteStub}, nil
		},
	}
	regIncomplete, err := conditions.NewDefaultRegistry(incompleteDeps)
	testutil.FailErr(t, "build conditions registry", err)
	for _, id := range []string{"scope_missing", "breaking_missing", "review_depth_missing"} {
		ok, err := regIncomplete.Evaluate(id, conditions.EvalContext{BlueprintPath: "p1"})
		if err != nil {
			t.Fatalf("%s err=%v", id, err)
		}
		if !ok {
			t.Fatalf("%s = false want true for incomplete stub plan content", id)
		}
	}
}

func TestPlanDomainPredicatesRegistered(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	for _, id := range []string{
		"scope_missing",
		"breaking_missing",
		"review_depth_missing",
	} {
		_, err := reg.Evaluate(id, conditions.EvalContext{})
		if err != nil {
			t.Fatalf("%s err=%v", id, err)
		}
	}
}
