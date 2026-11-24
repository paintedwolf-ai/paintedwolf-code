package assembly

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

func (e *promptSurface) repoKnownEmpty(ctx context.Context, workspacePath string) bool {
	fn := e.deps.RepoKnownEmpty
	return fn != nil && fn(ctx, workspacePath)
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

// turnReader is the Reader of the coordinator request this assembly precedes,
// projected through the live turn pipeline.
func (e *promptSurface) turnReader(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	history []api.Message,
	turn *TurnAssemblyScratch,
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
	reader, err := e.requestReader(ctx, sess, history, frame)
	if err != nil {
		return prompts.Reader{}, err
	}
	if turn != nil {
		turn.Reader = reader
	}
	return reader, nil
}

// requestReader projects sess's request through the deps' turn pipeline.
func (e *promptSurface) requestReader(ctx context.Context, sess *api.Session, history []api.Message, frame inject.CoordinatorTurnFrame) (prompts.Reader, error) {
	if e == nil || e.deps.TurnReader == nil {
		return prompts.Reader{}, errTurnReaderNotConfigured
	}
	reader, err := e.deps.TurnReader(ctx, sess, history, frame)
	if err != nil {
		return prompts.Reader{}, err
	}
	return reader.WithVision(e.deps.ModelVision != nil && e.deps.ModelVision(ctx, sess)), nil
}
