package sessions

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func TestSkillSurfaceInputPreservesRuntimePlan(t *testing.T) {
	for _, plan := range []toolsurface.Plan{toolsurface.Compile(nil, nil), toolsurface.Compile([]string{"skills_read"}, []string{"fetch_url"})} {
		got := skillSurfaceVarsInput(tools.ToolContext{Identity: tools.InvocationIdentity{Agent: "coordinator"}, Turn: tools.InvocationTurn{TurnToolPlan: plan}}, spawn.DefaultWorkerToolBudget())
		if !got.Plan.Compiled() || !slices.Equal(got.Plan.ImmediateNames(), plan.ImmediateNames()) || !slices.Equal(got.Plan.DeferredNames(), plan.DeferredNames()) {
			t.Fatalf("skill input changed plan: %+v", got.Plan)
		}
	}
}
