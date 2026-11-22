package assembly

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/gitrepo"
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
		if sess.Mode == api.SessionModeAsk && !tools.AllowedInAsk(meta) {
			continue
		}
		if strings.TrimSpace(meta.Name) != "" {
			names = append(names, meta.Name)
		}
	}
	vars["visible_tools"] = names
	// A persona renders the units and loaded tools of its own session.
	vars["loaded_tools"] = e.loadedTools(sess)
}
func (e *promptSurface) effectivePromptSurface(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	turn *TurnAssemblyScratch,
) (prompts.AgentPromptSurface, bool) {
	if e == nil || sess == nil {
		return prompts.AgentPromptSurface{}, false
	}
	var surface prompts.AgentPromptSurface
	switch {
	case frame.Machine.Compiled():
		surface = frame.Machine.Surface
	case turn != nil && turn.PromptSurfaceLoaded:
		surface = turn.PromptSurface
	case e.deps.EffectivePromptSurface != nil:
		surface = e.deps.EffectivePromptSurface(ctx, sess)
		if turn != nil {
			turn.PromptSurface = surface
			turn.PromptSurfaceLoaded = true
		}
	default:
		return prompts.AgentPromptSurface{}, false
	}
	if e.deps.Selection != nil {
		surface.FullCatalog = e.deps.Selection.FullCatalog(sess.ID)
		surface.Fingerprint = surface.CacheFingerprint()
	}
	return surface, true
}

func (e *promptSurface) mergeEffectivePromptSurfaceWithTurn(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	turn *TurnAssemblyScratch,
	vars map[string]any,
) {
	if vars == nil {
		return
	}
	surface, ok := e.effectivePromptSurface(ctx, sess, frame, turn)
	if !ok {
		return
	}
	for key, value := range prompts.AgentPromptSurfaceTemplateVars(surface) {
		switch value := value.(type) {
		case []string:
			vars[key] = value
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

func (e *promptSurface) onWorkerBranch(ctx context.Context, sess *api.Session) (string, bool) {
	if e == nil || e.deps.OnWorkerBranch == nil || sess == nil {
		return "", false
	}
	return e.deps.OnWorkerBranch(ctx, sess)
}

func readerGitFacts(roots []map[string]any, activePath, fallbackWorkspacePath, workerBranchRoot string) (hasGit, hasAnyGitRoot bool) {
	primaryPath := strings.TrimSpace(activePath)
	if primaryPath == "" {
		for _, row := range roots {
			if active, _ := row["is_active"].(bool); active {
				if p, _ := row["path"].(string); strings.TrimSpace(p) != "" {
					primaryPath = strings.TrimSpace(p)
					break
				}
			}
		}
	}
	if primaryPath == "" {
		for _, row := range roots {
			if primary, _ := row["is_primary"].(bool); primary {
				if p, _ := row["path"].(string); strings.TrimSpace(p) != "" {
					primaryPath = strings.TrimSpace(p)
					break
				}
			}
		}
	}
	if primaryPath == "" && len(roots) > 0 {
		if p, _ := roots[0]["path"].(string); strings.TrimSpace(p) != "" {
			primaryPath = strings.TrimSpace(p)
		}
	}
	if primaryPath == "" {
		primaryPath = strings.TrimSpace(fallbackWorkspacePath)
	}
	if primaryPath != "" {
		_, hasGit = gitrepo.Discover(primaryPath)
	}
	if !hasGit && strings.TrimSpace(workerBranchRoot) != "" {
		_, hasGit = gitrepo.Discover(strings.TrimSpace(workerBranchRoot))
	}
	hasAnyGitRoot = hasGit
	if !hasAnyGitRoot {
		for _, row := range roots {
			if p, _ := row["path"].(string); strings.TrimSpace(p) != "" {
				if _, ok := gitrepo.Discover(strings.TrimSpace(p)); ok {
					hasAnyGitRoot = true
					break
				}
			}
		}
	}
	return hasGit, hasAnyGitRoot
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
	if sess.Mode == api.SessionModeAsk {
		declared = prompts.ReadOnlyAgents(declared)
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
	var mode api.SessionMode
	if sess != nil {
		mode = sess.Mode
	}
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID, SessionMode: mode}, rootCount)
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
	profile, _ := e.resolveCoordinatorProfile(ctx, sess, frame, history, turn)
	if frame.Roster != nil && strings.TrimSpace(frame.Roster.Facts.SurfaceID) != "" {
		profile.SurfaceID = strings.TrimSpace(frame.Roster.Facts.SurfaceID)
	}
	// A turn that moves to another surface, as a dispatch moves it to park,
	// is read with that surface's tools.
	if turn != nil && turn.Reader.Valid() && turn.Reader.SurfaceID() == profile.SurfaceID {
		return turn.Reader, nil
	}
	roots, rootCount, activePath, _, ok := e.workspaceRootsForTurn(ctx, sess, turn)
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
	sessionMode := string(api.SessionModeAgent)
	var fallbackPath string
	if sess != nil {
		if sess.Mode == api.SessionModeAsk {
			sessionMode = string(api.SessionModeAsk)
		}
		fallbackPath = sess.WorkspacePath
	}
	branchRoot, onWorkerBranch := e.onWorkerBranch(ctx, sess)
	hasGit, hasAnyGitRoot := readerGitFacts(roots, activePath, fallbackPath, branchRoot)
	var spawnAllowlist []string
	if frame.Roster != nil {
		spawnAllowlist = frame.Roster.Effective
	} else {
		declared := frame.RunContext.AllowedAgents
		if len(declared) == 0 {
			declared = spawn.AmbientAllowedAgents()
		}
		spawnAllowlist = declared
	}
	var (
		reviewsConfinedWrites bool
		externalHidden        bool
		scratchDir            string
		sharedTemp            []string
	)
	if promptSurf, hasSurf := e.effectivePromptSurface(ctx, sess, frame, turn); hasSurf {
		if promptSurf.Posture != "" {
			reviewsConfinedWrites = promptSurf.Posture.ReviewsConfinedWrites()
		}
		externalHidden = promptSurf.CommandExternalHidden
		scratchDir = promptSurf.SessionScratchDir
		sharedTemp = promptSurf.SharedTempLocations
	}
	visionEnabled := e.deps.ModelVision != nil && e.deps.ModelVision(ctx, sess)
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID:             prompts.CoordinatorProfileID,
		SurfaceID:             profile.SurfaceID,
		Mode:                  surface.ExecutionModeFamily(profile.SurfaceID),
		SessionMode:           sessionMode,
		Profiles:              profiles,
		Plan:                  plan,
		Loaded:                e.loadedTools(sess),
		Schemas:               schemas,
		RootCount:             rootCount,
		HasGit:                hasGit,
		HasAnyGitRoot:         hasAnyGitRoot,
		OnWorkerBranch:        onWorkerBranch,
		WebSearch:             webSearchEnabled,
		SpawnAllowlist:        spawnAllowlist,
		ReviewsConfinedWrites: reviewsConfinedWrites,
		CommandExternalHidden: externalHidden,
		SessionScratchDir:     scratchDir,
		SharedTempLocations:   sharedTemp,
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
