package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

func askToolArgumentSchemas(metas []tools.ToolMeta) []tools.ToolMeta {
	out := append([]tools.ToolMeta(nil), metas...)
	for index, meta := range out {
		if meta.Name != "ask_user" {
			continue
		}
		out[index].ArgsSchema = jsonvalue.CloneMap(meta.ArgsSchema)
		properties, _ := out[index].ArgsSchema["properties"].(map[string]any)
		if response, ok := properties["response_type"].(map[string]any); ok {
			response["enum"] = []any{"text", "single_choice", "multi_choice"}
			response["description"] = "Kind of response. Defaults to text."
		}
		delete(properties, "secret")
	}
	return out
}

func (l *promptContext) askEnabledMCPMetas(ctx context.Context, sess *api.Session, metas []tools.ToolMeta) []tools.ToolMeta {
	if sess == nil || sess.Mode != api.SessionModeAsk || l.Deps.MCPAlwaysLoad == nil {
		return metas
	}
	enabled := l.Deps.MCPAlwaysLoad(ctx, sess)
	out := make([]tools.ToolMeta, 0, len(metas))
	for _, meta := range metas {
		_, enabledProvider := enabled[meta.SourceID]
		if !meta.IsMCP() || enabledProvider {
			out = append(out, meta)
		}
	}
	return out
}

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
	return prompts.ReadOnlyAgents(agents)
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
