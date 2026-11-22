package assembly

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolscope"
	"github.com/lycaon/lycaon/pkg/api"
)

// promptSurface resolves and renders one immutable host wiring snapshot.
type promptSurface struct {
	deps  promptSurfaceDeps
	cache *SessionPromptCache
}

func (e *promptSurface) resolveSystemPromptRef(ctx context.Context, sess *api.Session) (string, error) {
	coordinatorProfile := ""
	if e != nil && e.deps.CoordinatorProfile != nil {
		coordinatorProfile = e.deps.CoordinatorProfile(ctx, sess.ID)
	}
	return ResolveSystemPromptTemplate(sess, e.agentsForSession(ctx, sess), coordinatorProfile), nil
}
func (e *promptSurface) agentsForSession(ctx context.Context, sess *api.Session) AgentProfileResolver {
	if view := e.sessionCatalogView(ctx, sess); view != nil {
		return view
	}
	return e.deps.Agents
}
func (e *promptSurface) projectPrompts(ctx context.Context, sess *api.Session) prompts.PromptTemplateEngine {
	pe := e.deps.Prompts
	fe, ok := pe.(*prompts.FileTemplateEngine)
	if !ok || fe == nil || sess == nil {
		return pe
	}
	if archive := e.deps.WorkflowArchive; archive != nil {
		fe = fe.WithWorkflowArchive(archive(ctx, sess.ID))
	}
	// Empty trusted roots disable project prompt layers.
	if resolve := e.deps.ProjectOverlayRootPaths; resolve != nil {
		paths := resolve(ctx, sess)
		if len(paths) == 0 {
			return e.attachSessionCatalog(ctx, sess, fe)
		}
		return e.attachSessionCatalog(ctx, sess, fe.WithProjectOverlays(paths))
	}
	return e.attachSessionCatalog(ctx, sess, fe.WithProjectOverlay(sess.WorkspacePath))
}
func (e *promptSurface) projectPromptsSnapshot(ctx context.Context, sess *api.Session) (prompts.PromptTemplateEngine, string, error) {
	pe := e.projectPrompts(ctx, sess)
	if pe == nil {
		return nil, "", nil
	}
	fe, ok := pe.(*prompts.FileTemplateEngine)
	if !ok {
		return pe, "", nil
	}
	snapshot, err := fe.Snapshot()
	if err != nil {
		return nil, "", fmt.Errorf("prompt source snapshot: %w", err)
	}
	revision := snapshot.Revision()
	return snapshot, revision, nil
}
func (e *promptSurface) attachSessionCatalog(ctx context.Context, sess *api.Session, fe *prompts.FileTemplateEngine) *prompts.FileTemplateEngine {
	if e == nil || fe == nil {
		return fe
	}
	if resolve := e.deps.SessionView; resolve != nil {
		if view := resolve(ctx, sess); view != nil && view.Catalog != nil {
			return fe.WithEffectiveCatalog(view.Catalog)
		}
		return fe
	}
	return fe
}
func (e *promptSurface) sessionCatalogView(ctx context.Context, sess *api.Session) *catalogview.View {
	if e == nil {
		return nil
	}
	if resolve := e.deps.SessionView; resolve != nil {
		return resolve(ctx, sess)
	}
	return nil
}
func (e *promptSurface) renderSystemPromptWithEngine(
	ctx context.Context,
	pe prompts.PromptTemplateEngine,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	turn *TurnAssemblyScratch,
	templateRef string,
	vars map[string]any,
) (string, error) {
	if e == nil || pe == nil {
		return "", errPromptEngineNotConfigured
	}
	agentType := ""
	if sess != nil {
		agentType = strings.TrimSpace(sess.AgentType)
	}
	if fe, ok := pe.(*prompts.FileTemplateEngine); ok && agentType != "" {
		reader, err := e.workerReader(ctx, sess, frame, turn, fe, agentType, vars)
		if err == nil {
			out, renderErr := prompts.RenderPersona(ctx, fe, agentType, reader, vars)
			if renderErr == nil {
				return out, nil
			}
			if !errors.Is(renderErr, prompts.ErrUnknownAgent) {
				return "", renderErr
			}
		} else if !errors.Is(err, prompts.ErrUnknownAgent) {
			return "", err
		}
	}
	return pe.Render(ctx, templateRef, vars)
}

func (e *promptSurface) workerReader(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	turn *TurnAssemblyScratch,
	fe *prompts.FileTemplateEngine,
	agentType string,
	vars map[string]any,
) (prompts.Reader, error) {
	profileID := ""
	if agents := e.agentsForSession(ctx, sess); agents != nil {
		if profile, err := agents.Get(agentType); err == nil {
			profileID = strings.TrimSpace(profile.ToolProfile)
		}
	}
	if profileID == "" {
		var err error
		profileID, err = prompts.ToolProfileForAgent(agentType)
		if err != nil {
			return prompts.Reader{}, err
		}
	}
	var (
		profiles []sandbox.ToolProfile
		schemas  *toolschema.Config
		catalog  *extpacks.EffectiveCatalog
	)
	if view := e.sessionCatalogView(ctx, sess); view != nil {
		profiles = view.ToolProfiles
		schemas = view.ToolSchemas
		catalog = view.Catalog
	}
	if catalog == nil && fe != nil {
		catalog = fe.Layers().Catalog
	}
	if len(profiles) == 0 {
		var (
			loaded []sandbox.ToolProfile
			err    error
		)
		if catalog != nil {
			loaded, err = sandbox.LoadToolProfilesWithCatalog(catalog)
		} else {
			loaded, err = sandbox.LoadToolProfiles()
		}
		if err != nil {
			return prompts.Reader{}, err
		}
		profiles = loaded
	}
	var visible []string
	var loaded map[string]bool
	roots, rootCount, activePath, _, ok := e.workspaceRootsForTurn(ctx, sess, turn)
	if !ok && sess != nil && strings.TrimSpace(sess.WorkspacePath) != "" {
		rootCount = 1
	}
	if vars != nil {
		if raw, ok := vars["visible_tools"].([]string); ok {
			visible = raw
		}
		if raw, ok := vars["loaded_tools"].(map[string]bool); ok {
			loaded = raw
		}
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
	webSearchEnabled := true
	if fn := e.deps.WebSearchEnabled; fn != nil {
		webSearchEnabled = fn()
	}
	visionEnabled := e.deps.ModelVision != nil && e.deps.ModelVision(ctx, sess)
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID:             profileID,
		SessionMode:           sessionMode,
		Profiles:              profiles,
		Loaded:                loaded,
		Visible:               visible,
		Schemas:               schemas,
		RootCount:             rootCount,
		HasGit:                hasGit,
		HasAnyGitRoot:         hasAnyGitRoot,
		OnWorkerBranch:        onWorkerBranch,
		WebSearch:             webSearchEnabled,
		ReviewsConfinedWrites: reviewsConfinedWrites,
		CommandExternalHidden: externalHidden,
		SessionScratchDir:     scratchDir,
		SharedTempLocations:   sharedTemp,
	})
	if err != nil {
		return prompts.Reader{}, err
	}
	return reader.WithVision(visionEnabled), nil
}

func (e *promptSurface) workerTurnReader(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	turn *TurnAssemblyScratch,
) (prompts.Reader, error) {
	agentType := ""
	if sess != nil {
		agentType = strings.TrimSpace(sess.AgentType)
	}
	if agentType == "" {
		agentType = "implementer"
	}
	vars := map[string]any{}
	e.mergeVisibleTools(ctx, sess, frame, vars)
	fe, _ := e.projectPrompts(ctx, sess).(*prompts.FileTemplateEngine)
	return e.workerReader(ctx, sess, frame, turn, fe, agentType, vars)
}

func filterImpliedJobControls(sess *api.Session, profile sandbox.ToolProfile, rootCount int, implied []string) []string {
	ask := sess != nil && sess.Mode == api.SessionModeAsk
	var allowed []string
	for _, name := range implied {
		if !profile.ToolAllowed(name) {
			continue
		}
		if rootCount == 0 && toolscope.RequiresProjectRoots(name) {
			continue
		}
		if ask && !tools.AllowedInAsk(tools.ToolMeta{Name: name}) {
			continue
		}
		allowed = append(allowed, name)
	}
	return allowed
}

func (e *promptSurface) commandJobsReader(
	ctx context.Context,
	sess *api.Session,
	coordinator bool,
	frame inject.CoordinatorTurnFrame,
	history []api.Message,
	turn *TurnAssemblyScratch,
	jobs []bgprocess.JobSnapshot,
	held []heldcall.Running,
) (prompts.Reader, []string, error) {
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
			return prompts.Reader{}, nil, err
		}
		profiles = loaded
	}
	implied := toolcontract.Implied(toolcontract.ResourcePresence{
		CommandJobs: len(jobs) > 0,
		HeldCalls:   len(held) > 0,
	})
	if coordinator {
		return e.coordinatorCommandJobsReader(ctx, sess, frame, history, turn, profiles, schemas, implied)
	}
	return e.workerCommandJobsReader(ctx, sess, frame, turn, profiles, implied)
}

func (e *promptSurface) coordinatorCommandJobsReader(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	history []api.Message,
	turn *TurnAssemblyScratch,
	profiles []sandbox.ToolProfile,
	schemas *toolschema.Config,
	implied []string,
) (prompts.Reader, []string, error) {
	base, err := e.turnReader(ctx, sess, frame, history, turn, 0)
	if err != nil {
		return prompts.Reader{}, nil, err
	}
	var coordProfile sandbox.ToolProfile
	for _, p := range profiles {
		if p.ID == prompts.CoordinatorProfileID {
			coordProfile = p
			break
		}
	}
	waitConds := slices.Clone(coordProfile.WaitConditions)
	promoted := filterImpliedJobControls(sess, coordProfile, base.RootCount(), implied)
	needsRebuild := false
	for _, name := range promoted {
		if !slices.Contains(base.Offered(), name) {
			needsRebuild = true
			break
		}
	}
	if !needsRebuild {
		return base, waitConds, nil
	}
	turnProf, _ := e.resolveCoordinatorProfile(ctx, sess, frame, history, turn)
	if base.SurfaceID() != "" {
		turnProf.SurfaceID = base.SurfaceID()
	}
	plan, err := surface.CompileToolPlan(turnProf, base.RootCount())
	if err != nil {
		return base, waitConds, nil
	}
	plan = plan.Promote(promoted...)
	roots, _, activePath, _, _ := e.workspaceRootsForTurn(ctx, sess, turn)
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
	switch {
	case frame.Roster != nil:
		spawnAllowlist = frame.Roster.Effective
	case len(frame.RunContext.AllowedAgents) > 0:
		spawnAllowlist = frame.RunContext.AllowedAgents
	default:
		spawnAllowlist = spawn.AmbientAllowedAgents()
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
	rebuilt, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID:             prompts.CoordinatorProfileID,
		SurfaceID:             turnProf.SurfaceID,
		Mode:                  surface.ExecutionModeFamily(turnProf.SurfaceID),
		SessionMode:           sessionMode,
		Profiles:              profiles,
		Plan:                  plan,
		Loaded:                e.loadedTools(sess),
		Schemas:               schemas,
		RootCount:             base.RootCount(),
		HasGit:                hasGit,
		HasAnyGitRoot:         hasAnyGitRoot,
		OnWorkerBranch:        onWorkerBranch,
		WebSearch:             base.WebSearch(),
		SpawnAllowlist:        spawnAllowlist,
		ReviewsConfinedWrites: reviewsConfinedWrites,
		CommandExternalHidden: externalHidden,
		SessionScratchDir:     scratchDir,
		SharedTempLocations:   sharedTemp,
	})
	if err != nil {
		return base, waitConds, nil
	}
	visionEnabled := e.deps.ModelVision != nil && e.deps.ModelVision(ctx, sess)
	return rebuilt.WithVision(visionEnabled), waitConds, nil
}

func (e *promptSurface) workerCommandJobsReader(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	turn *TurnAssemblyScratch,
	profiles []sandbox.ToolProfile,
	implied []string,
) (prompts.Reader, []string, error) {
	base, err := e.workerTurnReader(ctx, sess, frame, turn)
	if err != nil {
		return prompts.Reader{}, nil, err
	}
	var workerProf sandbox.ToolProfile
	for _, p := range profiles {
		if p.ID == base.ProfileID() {
			workerProf = p
			break
		}
	}
	waitConds := slices.Clone(workerProf.WaitConditions)
	promoted := filterImpliedJobControls(sess, workerProf, base.RootCount(), implied)
	needsRebuild := false
	for _, name := range promoted {
		if !slices.Contains(base.Offered(), name) {
			needsRebuild = true
			break
		}
	}
	if !needsRebuild {
		return base, waitConds, nil
	}
	vars := map[string]any{}
	e.mergeVisibleTools(ctx, sess, frame, vars)
	loadedMap := maps.Clone(e.loadedTools(sess))
	if loadedMap == nil {
		loadedMap = map[string]bool{}
	}
	for _, name := range promoted {
		loadedMap[name] = true
	}
	vars["loaded_tools"] = loadedMap
	if vis, ok := vars["visible_tools"].([]string); ok {
		for _, name := range promoted {
			if !slices.Contains(vis, name) {
				vis = append(vis, name)
			}
		}
		vars["visible_tools"] = vis
	}
	agentType := ""
	if sess != nil {
		agentType = strings.TrimSpace(sess.AgentType)
	}
	if agentType == "" {
		agentType = "implementer"
	}
	fe, _ := e.projectPrompts(ctx, sess).(*prompts.FileTemplateEngine)
	rebuilt, err := e.workerReader(ctx, sess, frame, turn, fe, agentType, vars)
	if err != nil {
		return base, waitConds, nil
	}
	return rebuilt, waitConds, nil
}
