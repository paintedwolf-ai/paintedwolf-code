package conditions_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPlanStubValidReadsSyncedDBContent(t *testing.T) {
	body := "---\ntitle: Ship it\nresearch_depth: none\n---\n## Goal\n\ngame\n\n## Assumptions\n\na\n\n## Plan implementation scope\n\n**Size:** small\n\n## Plan breaking changes\n\nNone.\n\n## Approach\n\nship it\n"
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		BlueprintGet: func(_ context.Context, _ string) (*api.Blueprint, error) {
			return &api.Blueprint{Path: "plan-uuid", Title: "plan", Content: body}, nil
		},
	})
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{
		Ctx:           context.Background(),
		BlueprintPath: "plan-uuid",
		ProjectDir:    t.TempDir(),
		WorkflowID:    "plan",
	}
	ok, err := reg.Evaluate("plan_stub_valid", ec)
	if err != nil || !ok {
		t.Fatalf("plan_stub_valid = %v err=%v", ok, err)
	}
}
