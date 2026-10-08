package assembly

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func (e *promptSurface) repoKnownEmpty(ctx context.Context, workspacePath string) bool {
	fn := e.wiring.RepoKnownEmpty
	return fn != nil && fn(ctx, workspacePath)
}
func (e *promptSurface) mergeVisibleTools(ctx context.Context, sess *api.Session, frame inject.CoordinatorTurnFrame, vars map[string]any) {
	if e == nil || vars == nil || sess == nil || e.wiring.PromptToolLister == nil {
		return
	}
	profileID := strings.TrimSpace(frame.Machine.ProfileID)
	if agents := e.agentsForSession(ctx, sess); profileID == "" && agents != nil {
		if profile, err := agents.Get(sess.AgentType); err == nil {
			profileID = profile.ToolProfile
		}
	}
	if profileID == "" {
		fe, ok := e.wiring.Prompts.(*prompts.FileTemplateEngine)
		if !ok || fe == nil {
			return
		}
		var err error
		profileID, err = prompts.ToolProfileForAgent(sess.AgentType)
		if err != nil {
			return
		}
	}
	metas, err := e.wiring.PromptToolLister(ctx, sess, profileID)
	if err != nil || len(metas) == 0 {
		return
	}
	if frame.Machine.Compiled() {
		metas = tools.HideSkillsReadWhenEmpty(metas, frame.Machine.SkillCount)
	}
	names := make([]string, 0, len(metas))
	for _, meta := range metas {
		if strings.TrimSpace(meta.Name) != "" {
			names = append(names, meta.Name)
		}
	}
	vars["visible_tools"] = names
	// A persona renders the units and loaded tools of its own session.
	vars["loaded_tools"] = e.loadedTools(sess)
	vars["omitted_units"] = e.omittedUnits(sess)
}
func (e *promptSurface) mergeEffectivePromptSurface(ctx context.Context, sess *api.Session, frame inject.CoordinatorTurnFrame, vars map[string]any) {
	if e == nil || sess == nil || vars == nil {
		return
	}
	var surface prompts.AgentPromptSurface
	switch {
	case frame.Machine.Compiled():
		surface = frame.Machine.Surface
	case e.wiring.EffectivePromptSurface != nil:
		surface = e.wiring.EffectivePromptSurface(ctx, sess)
	default:
		return
	}
	for key, value := range prompts.AgentPromptSurfaceTemplateVars(surface) {
		switch value := value.(type) {
		case []map[string]any:
			if len(value) > 0 {
				vars[key] = value
			}
		case string:
			if strings.TrimSpace(value) != "" {
				vars[key] = value
			}
		case int:
			// Preserve the full count when the roster is truncated.
			if value > 0 {
				vars[key] = value
			}
		}
	}
}
func (e *promptSurface) mergeVisibleToolCapabilityVars(vars map[string]any) {
	if vars == nil {
		return
	}
	rootCount := 0
	if v, ok := vars["root_count"].(int); ok {
		rootCount = v
	}
	var visible []string
	switch v := vars["visible_tools"].(type) {
	case []string:
		visible = v
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				visible = append(visible, s)
			}
		}
	}
	capability.MergeVars(vars, capability.Derive(rootCount, visible, nil))
}
func (e *promptSurface) workerToolBudget(ctx context.Context, sess *api.Session) spawn.WorkerToolBudget {
	if deps := e.wiring; deps.Limits != nil {
		return deps.Limits(ctx, sess).WorkerToolBudget()
	}
	return spawn.DefaultWorkerToolBudget()
}
func (e *promptSurface) applyToolBudgetVars(ctx context.Context, sess *api.Session, coordinator bool, vars map[string]any) {
	budget := e.workerToolBudget(ctx, sess)
	if sess.IsWorkerChild() {
		vars["max_tool_loops"] = budget.Effective(sess.MaxToolLoops)
	}
	if coordinator {
		vars["worker_tool_budget_default"] = budget.Default
		vars["worker_tool_budget_min"] = budget.Min
		vars["worker_tool_budget_max"] = budget.Max
	}
}
func (e *promptSurface) workspaceRootsForTurn(ctx context.Context, sess *api.Session, turn *TurnAssemblyScratch) ([]map[string]any, int, string, bool) {
	if turn != nil && turn.WorkspaceRootsLoaded {
		return turn.WorkspaceRoots, turn.WorkspaceRootCount, turn.WorkspaceActivePath, true
	}
	load := e.wiring.WorkspaceRoots
	if load == nil {
		return nil, 0, "", false
	}
	roots, count, activePath := load(ctx, sess)
	if count < 0 {
		return nil, 0, "", false
	}
	if turn != nil {
		turn.WorkspaceRoots = roots
		turn.WorkspaceRootCount = count
		turn.WorkspaceActivePath = activePath
		turn.WorkspaceRootsLoaded = true
	}
	return roots, count, activePath, true
}
func (e *promptSurface) resolveTurnRoster(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	history []api.Message,
	turn *TurnAssemblyScratch,
) *inject.AgentRoster {
	profile, _ := e.resolveCoordinatorProfile(ctx, sess, frame, history, turn)
	_, rootCount, _, _ := e.workspaceRootsForTurn(ctx, sess, turn)
	repoKnownEmpty := false
	if path := strings.TrimSpace(sess.WorkspacePath); path != "" {
		repoKnownEmpty = e.repoKnownEmpty(ctx, path)
	}
	webSearchEnabled := true
	if fn := e.wiring.WebSearchEnabled; fn != nil {
		webSearchEnabled = fn()
	}
	declared := frame.RunContext.AllowedAgents
	if len(declared) == 0 {
		declared = spawn.AmbientAllowedAgents()
	}
	roster := inject.ResolveAgentRoster(profile.SurfaceID, declared, rootCount, repoKnownEmpty, webSearchEnabled)
	return &roster
}
func (e *promptSurface) taskOffered(sess *api.Session, surfaceID string, rootCount int) bool {
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID}, rootCount)
	if err != nil {
		return false
	}
	if plan.Immediate("task") {
		return true
	}
	return plan.Deferred("task") && e.loadedTools(sess)["task"]
}
