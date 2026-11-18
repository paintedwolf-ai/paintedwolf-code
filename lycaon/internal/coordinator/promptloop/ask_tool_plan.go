package promptloop

import (
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

func clampAskMode(plan toolsurface.Plan, metas []tools.ToolMeta) toolsurface.Plan {
	allowed := make(map[string]bool, len(metas))
	for _, meta := range metas {
		allowed[meta.Name] = tools.AllowedInAsk(meta)
	}
	for _, name := range plan.AddressableNames() {
		if !allowed[name] {
			plan = plan.Without(name)
		}
	}
	return plan
}

func readOnlySpawnAllowlist(agents []string) []string {
	if agents == nil {
		agents = spawn.AmbientAllowedAgents()
	}
	out := make([]string, 0, len(agents))
	for _, agent := range agents {
		if writes, known := prompts.AgentMutationCapable(agent); known && !writes {
			out = append(out, agent)
		}
	}
	return out
}

func addAskWorkflowTools(plan toolsurface.Plan, run api.CoordinatorRunContext, rootCount int) toolsurface.Plan {
	if run.RunID == "" || run.PhaseCoordinatorSurface == "" {
		return plan
	}
	phase, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: run.PhaseCoordinatorSurface}, rootCount)
	if err != nil {
		return plan
	}
	plan = plan.Promote(phase.ImmediateNames()...)
	for _, name := range phase.DeferredNames() {
		if !plan.Addressable(name) {
			plan = plan.Defer(name)
		}
	}
	return plan
}
