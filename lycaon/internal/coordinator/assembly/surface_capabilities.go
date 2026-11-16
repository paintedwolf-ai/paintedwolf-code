package assembly

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
)

func (e *promptSurface) repoKnownEmpty(ctx context.Context, workspacePath string) bool {
	fn := e.deps.RepoKnownEmpty
	return fn != nil && fn(ctx, workspacePath)
}
func (e *promptSurface) mergeVisibleTools(ctx context.Context, sess *api.Session, frame inject.CoordinatorTurnFrame, vars map[string]any) {
	if e == nil || vars == nil || sess == nil || e.deps.PromptToolLister == nil {
		return
	}
	profileID := strings.TrimSpace(frame.Machine.ProfileID)
	if agents := e.agentsForSession(ctx, sess); profileID == "" && agents != nil {
		if profile, err := agents.Get(sess.AgentType); err == nil {
			profileID = profile.ToolProfile
		}
	}
	if profileID == "" {
		fe, ok := e.deps.Prompts.(*prompts.FileTemplateEngine)
		if !ok || fe == nil {
			return
		}
		var err error
		profileID, err = prompts.ToolProfileForAgent(sess.AgentType)
		if err != nil {
			return
		}
	}
	metas, err := e.deps.PromptToolLister(ctx, sess, profileID)
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
}
func (e *promptSurface) mergeEffectivePromptSurface(ctx context.Context, sess *api.Session, frame inject.CoordinatorTurnFrame, vars map[string]any) {
	if e == nil || sess == nil || vars == nil {
		return
	}
	var surface prompts.AgentPromptSurface
	switch {
	case frame.Machine.Compiled():
		surface = frame.Machine.Surface
	case e.deps.EffectivePromptSurface != nil:
		surface = e.deps.EffectivePromptSurface(ctx, sess)
	default:
		return
	}
	if e.deps.Selection != nil {
		surface.FullCatalog = e.deps.Selection.FullCatalog(sess.ID)
		surface.Fingerprint = surface.CacheFingerprint()
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
		case bool:
			vars[key] = value
		}
	}
}
func (e *promptSurface) mergeVisibleToolCapabilityVars(ctx context.Context, sess *api.Session, turn *TurnAssemblyScratch, vars map[string]any) {
	if vars == nil {
		return
	}
	_, rootCount, _, _, _ := e.workspaceRootsForTurn(ctx, sess, turn)
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
	if deps := e.deps; deps.Limits != nil {
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
func (e *promptSurface) workspaceRootsForTurn(ctx context.Context, sess *api.Session, turn *TurnAssemblyScratch) ([]map[string]any, int, string, int, bool) {
	if turn != nil && turn.WorkspaceRootsLoaded {
		return turn.WorkspaceRoots, turn.WorkspaceRootCount, turn.WorkspaceActivePath, turn.WorkspaceRootsOmittedCount, true
	}
	load := e.deps.WorkspaceRoots
	if load == nil {
		return nil, 0, "", 0, false
	}
	roots, count, activePath, omitted := load(ctx, sess)
	if count < 0 {
		return nil, 0, "", 0, false
	}
	if turn != nil {
		turn.WorkspaceRoots = roots
		turn.WorkspaceRootCount = count
		turn.WorkspaceActivePath = activePath
		turn.WorkspaceRootsOmittedCount = omitted
		turn.WorkspaceRootsLoaded = true
	}
	return roots, count, activePath, omitted, true
}

// mergeWorkspaceRootTurnVars applies the turn's workspace root snapshot to
// prompt vars for workspace-roots.md and the root-aware surfaces.
func mergeWorkspaceRootTurnVars(vars map[string]any, roots []map[string]any, activePath string, omittedCount int) {
	vars["workspace_roots"] = roots
	vars["workspace_roots_omitted_count"] = omittedCount
	if activePath != "" {
		vars["project_dir"] = activePath
	}
}
func (e *promptSurface) resolveTurnRoster(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	history []api.Message,
	turn *TurnAssemblyScratch,
) *inject.AgentRoster {
	profile, _ := e.resolveCoordinatorProfile(ctx, sess, frame, history, turn)
	_, rootCount, _, _, _ := e.workspaceRootsForTurn(ctx, sess, turn)
	repoKnownEmpty := false
	if path := strings.TrimSpace(sess.WorkspacePath); path != "" {
		repoKnownEmpty = e.repoKnownEmpty(ctx, path)
	}
	webSearchEnabled := true
	if fn := e.deps.WebSearchEnabled; fn != nil {
		webSearchEnabled = fn()
	}
	declared := frame.RunContext.AllowedAgents
	if len(declared) == 0 {
		declared = spawn.AmbientAllowedAgents()
	}
	roster := inject.ResolveAgentRoster(profile.SurfaceID, declared, rootCount, repoKnownEmpty, webSearchEnabled)
	return &roster
}

// toolAddressable answers from the compile the turn's plan starts from: the
// plan freezes after assembly, and nothing later adds a catalog tool the surface
// leaves out.
func (e *promptSurface) toolAddressable(ctx context.Context, sess *api.Session, frame inject.CoordinatorTurnFrame, history []api.Message, turn *TurnAssemblyScratch, tool string) bool {
	if e == nil {
		return false
	}
	profile, _ := e.resolveCoordinatorProfile(ctx, sess, frame, history, turn)
	_, rootCount, _, _, _ := e.workspaceRootsForTurn(ctx, sess, turn)
	plan, err := surface.CompileToolPlan(profile, rootCount)
	return err == nil && plan.Addressable(tool)
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

func (e *promptSurface) turnReader(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	history []api.Message,
	turn *TurnAssemblyScratch,
	fallbackRootCount int,
) (prompts.Reader, error) {
	if turn != nil && turn.Reader.Valid() {
		return turn.Reader, nil
	}
	profile, _ := e.resolveCoordinatorProfile(ctx, sess, frame, history, turn)
	if frame.Roster != nil && strings.TrimSpace(frame.Roster.Facts.SurfaceID) != "" {
		profile = surface.TurnProfile{SurfaceID: strings.TrimSpace(frame.Roster.Facts.SurfaceID)}
	}
	_, rootCount, _, _, ok := e.workspaceRootsForTurn(ctx, sess, turn)
	if !ok {
		switch {
		case fallbackRootCount > 0:
			rootCount = fallbackRootCount
		case frame.Roster != nil && frame.Roster.Facts.RootCount > 0:
			rootCount = frame.Roster.Facts.RootCount
		case sess != nil && strings.TrimSpace(sess.WorkspacePath) != "":
			rootCount = 1
		}
	}
	plan, err := surface.CompileToolPlan(profile, rootCount)
	if err != nil {
		return prompts.Reader{}, err
	}
	webSearchEnabled := true
	if frame.Roster != nil {
		webSearchEnabled = frame.Roster.Facts.WebSearchEnabled
	} else if fn := e.deps.WebSearchEnabled; fn != nil {
		webSearchEnabled = fn()
	}
	var (
		profiles []sandbox.ToolProfile
		schemas  *toolschema.Config
	)
	if view := e.sessionCatalogView(ctx, sess); view != nil {
		profiles = view.ToolProfiles
		schemas = view.ToolSchemas
	}
	if len(profiles) == 0 {
		loaded, err := sandbox.LoadToolProfiles()
		if err != nil {
			return prompts.Reader{}, err
		}
		profiles = loaded
	}
	visionEnabled := e.deps.ModelVision != nil && e.deps.ModelVision(ctx, sess)
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: profile.SurfaceID,
		Mode:      surface.ExecutionModeFamily(profile.SurfaceID),
		Profiles:  profiles,
		Plan:      plan,
		Loaded:    e.loadedTools(sess),
		Schemas:   schemas,
		RootCount: rootCount,
		WebSearch: webSearchEnabled,
	})
	if err != nil {
		return prompts.Reader{}, err
	}
	reader = reader.WithVision(visionEnabled)
	if turn != nil {
		turn.Reader = reader
	}
	return reader, nil
}
