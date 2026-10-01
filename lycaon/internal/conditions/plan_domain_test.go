package conditions_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPlanStubValid(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		BlueprintGet: func(_ context.Context, _ string) (*api.Blueprint, error) {
			return &api.Blueprint{Content: conditions.TestPlanContentWithTasks}, nil
		},
	})
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{BlueprintPath: "p1"}
	ok, err := reg.Evaluate("plan_stub_valid", ec)
	if err != nil || !ok {
		t.Fatalf("plan_stub_valid = %v err=%v", ok, err)
	}
}

func TestResearchSatisfiedSkipAndDepth(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	vars := map[string]any{"phase_skipped": map[string]any{"research": true}}
	ok, err := reg.Evaluate("research_satisfied", conditions.EvalContext{Vars: vars})
	if err != nil || !ok {
		t.Fatalf("skip: ok=%v err=%v", ok, err)
	}
	ok, err = reg.Evaluate("research_satisfied", conditions.EvalContext{Vars: map[string]any{}})
	if err != nil || !ok {
		t.Fatalf("no blueprint body: ok=%v err=%v", ok, err)
	}
	deps := conditions.RegistryDeps{
		BlueprintGet: func(_ context.Context, _ string) (*api.Blueprint, error) {
			return &api.Blueprint{Content: conditions.TestPlanContentWithTasks}, nil
		},
	}
	reg, err = conditions.NewDefaultRegistry(deps)
	testutil.FailErr(t, "build conditions registry", err)
	ok, err = reg.Evaluate("research_satisfied", conditions.EvalContext{BlueprintPath: "p1", Vars: map[string]any{}})
	if err != nil || !ok {
		t.Fatalf("depth none: ok=%v err=%v", ok, err)
	}
}

func TestReviewLoopActiveLeaf(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err := reg.Evaluate("review_loop_active", conditions.EvalContext{ReviewLoopActive: true})
	if err != nil || !ok {
		t.Fatalf("active: ok=%v err=%v", ok, err)
	}
	ok, err = reg.Evaluate("review_loop_active", conditions.EvalContext{})
	if err != nil || ok {
		t.Fatalf("inactive: ok=%v err=%v want false", ok, err)
	}
}

func TestResearchSatisfiedDurableAfterStamp(t *testing.T) {
	body := "---\ntitle: Ship it\nresearch_depth: light\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan implementation scope\n\n**Size:** s\n\n## Plan breaking changes\n\nnone\n\n## Approach\n\nship it\n"
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		BlueprintGet: func(_ context.Context, _ string) (*api.Blueprint, error) {
			return &api.Blueprint{Content: body}, nil
		},
	})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err := reg.Evaluate("research_satisfied", conditions.EvalContext{BlueprintPath: "p1", Vars: map[string]any{}})
	if err != nil || ok {
		t.Fatalf("before stamp: ok=%v err=%v want false", ok, err)
	}
	vars := map[string]any{"gates": map[string]any{"research_satisfied": true}}
	ok, err = reg.Evaluate("research_satisfied", conditions.EvalContext{BlueprintPath: "p1", Vars: vars})
	if err != nil || !ok {
		t.Fatalf("after stamp: ok=%v err=%v", ok, err)
	}
}

func TestImplementWorkflowReadyRequiresTasks(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		BlueprintGet: func(_ context.Context, _ string) (*api.Blueprint, error) {
			return &api.Blueprint{Content: "---\nresearch_depth: none\n---\n## Goal\n\n## Assumptions\n"}, nil
		},
	})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err := reg.Evaluate("implement_workflow_ready", conditions.EvalContext{
		BlueprintPath: "p1",
		Vars:          map[string]any{"gates": map[string]any{"human_approval": true}},
	})
	if err != nil || ok {
		t.Fatalf("expected not ready without tasks ok=%v err=%v", ok, err)
	}
	reg, err = conditions.NewDefaultRegistry(conditions.RegistryDeps{
		BlueprintGet: func(_ context.Context, _ string) (*api.Blueprint, error) {
			return &api.Blueprint{Content: conditions.TestPlanContentWithTasks}, nil
		},
	})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err = reg.Evaluate("implement_workflow_ready", conditions.EvalContext{
		BlueprintPath: "p1",
		Vars:          map[string]any{"gates": map[string]any{"human_approval": true}},
	})
	if err != nil || !ok {
		t.Fatalf("expected ready ok=%v err=%v", ok, err)
	}
}

func TestPlanAwaitingApproval(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		BlueprintGet: func(_ context.Context, _ string) (*api.Blueprint, error) {
			return &api.Blueprint{Content: conditions.TestPlanContentWithTasks}, nil
		},
	})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err := reg.Evaluate("plan_awaiting_approval", conditions.EvalContext{
		BlueprintPath: "p1",
		Phase:         "approve",
		Vars: map[string]any{
			"human_approval": map[string]any{"active": true},
		},
	})
	if err != nil || !ok {
		t.Fatalf("plan_awaiting_approval = %v err=%v", ok, err)
	}
}

func TestPlanAwaitingApprovalWithoutTasksMarker(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		BlueprintGet: func(_ context.Context, _ string) (*api.Blueprint, error) {
			return &api.Blueprint{Content: conditions.TestPlanContentStubOnly}, nil
		},
	})
	testutil.FailErr(t, "build conditions registry", err)
	ok, err := reg.Evaluate("plan_awaiting_approval", conditions.EvalContext{
		BlueprintPath: "p1",
		Phase:         "approve",
		Vars: map[string]any{
			"human_approval": map[string]any{"active": true},
		},
	})
	if err != nil || !ok {
		t.Fatalf("plan_awaiting_approval stub-only = %v err=%v", ok, err)
	}
}
