package assembly

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolpolicy"
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
			workerVars := vars
			if vars != nil && vars["agent_skills"] != nil {
				workerVars = maps.Clone(vars)
				filterOccurrenceSkillsForReader(fe, reader, workerVars)
			}
			out, renderErr := prompts.RenderPersona(ctx, fe, agentType, reader, workerVars)
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

type commandJobsToolLister struct {
	sess         *api.Session
	promptLister PromptToolLister
}

func (l *commandJobsToolLister) List(ctx context.Context, filter platform.ToolFilter) ([]tools.ToolMeta, error) {
	if l == nil || l.promptLister == nil {
		return nil, nil
	}
	metas, err := l.promptLister(ctx, l.sess, filter.ProfileID)
	if err != nil {
		return nil, err
	}
	if filter.ProjectRootCount > 0 {
		return metas, nil
	}
	out := make([]tools.ToolMeta, 0, len(metas))
	for _, meta := range metas {
		if toolscope.RequiresProjectRoots(meta.Name) {
			continue
		}
		out = append(out, meta)
	}
	return out, nil
}

func (e *promptSurface) resolveCommandJobsProfiles(ctx context.Context, sess *api.Session) ([]sandbox.ToolProfile, error) {
	var (
		profiles []sandbox.ToolProfile
		catalog  *extpacks.EffectiveCatalog
	)
	if view := e.sessionCatalogView(ctx, sess); view != nil {
		profiles = view.ToolProfiles
		catalog = view.Catalog
	}
	if catalog == nil {
		if fe, ok := e.projectPrompts(ctx, sess).(*prompts.FileTemplateEngine); ok && fe != nil {
			catalog = fe.Layers().Catalog
		}
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
			return nil, err
		}
		profiles = loaded
	}
	return profiles, nil
}

func (e *promptSurface) resolveCommandJobsProfile(
	ctx context.Context,
	sess *api.Session,
	coordinator bool,
	frame inject.CoordinatorTurnFrame,
	history []api.Message,
	turn *TurnAssemblyScratch,
) (string, inject.CoordinatorTurnFrame, error) {
	if coordinator {
		if frame.Roster == nil {
			frame.Roster = e.resolveTurnRoster(ctx, sess, frame, history, turn)
		}
		return prompts.CoordinatorProfileID, frame, nil
	}
	agentType := ""
	if sess != nil {
		agentType = strings.TrimSpace(sess.AgentType)
	}
	if agentType == "" {
		agentType = "implementer"
	}
	profileID := strings.TrimSpace(frame.Machine.ProfileID)
	if profileID == "" {
		if agents := e.agentsForSession(ctx, sess); agents != nil {
			if prof, err := agents.Get(agentType); err == nil {
				profileID = strings.TrimSpace(prof.ToolProfile)
			}
		}
	}
	if profileID == "" {
		var err error
		profileID, err = prompts.ToolProfileForAgent(agentType)
		if err != nil {
			return "", frame, err
		}
	}
	if !frame.Machine.Compiled() {
		frame.Machine = inject.Machine{ProfileID: profileID, SkillCount: 1}
	}
	return profileID, frame, nil
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
) (prompts.Reader, map[string]any, error) {
	profiles, err := e.resolveCommandJobsProfiles(ctx, sess)
	if err != nil {
		return prompts.Reader{}, nil, err
	}
	roots, rootCount, activePath, _, ok := e.workspaceRootsForTurn(ctx, sess, turn)
	if !ok {
		switch {
		case frame.Roster != nil && frame.Roster.Facts.RootCount > 0:
			rootCount = frame.Roster.Facts.RootCount
		case sess != nil && strings.TrimSpace(sess.WorkspacePath) != "":
			rootCount = 1
		}
	}
	profileID, frame, err := e.resolveCommandJobsProfile(ctx, sess, coordinator, frame, history, turn)
	if err != nil {
		return prompts.Reader{}, nil, err
	}

	webSearchEnabled := true
	if coordinator && frame.Roster != nil {
		webSearchEnabled = frame.Roster.Facts.WebSearchEnabled
	} else if fn := e.deps.WebSearchEnabled; fn != nil {
		webSearchEnabled = fn()
	}

	lister := &commandJobsToolLister{
		sess:         sess,
		promptLister: e.deps.PromptToolLister,
	}
	policy := toolpolicy.NewEngine(toolpolicy.EngineDeps{
		ToolLister:       lister,
		ProjectRootCount: func(context.Context, *api.Session) int { return rootCount },
	})
	pDeps := promptloop.ContextDeps{
		Policy:                policy,
		LoadedTools:           func(string) map[string]bool { return e.loadedTools(sess) },
		ImplementSessionState: e.deps.ImplementSessionState,
		ProjectRootCount:      func(context.Context, *api.Session) int { return rootCount },
		LiveResources: func(string) toolcontract.ResourcePresence {
			return toolcontract.ResourcePresence{
				CommandJobs: len(jobs) > 0,
				HeldCalls:   len(held) > 0,
			}
		},
		ContextSurfaceDeps: promptloop.ContextSurfaceDeps{
			WebSearchEnabled: func() bool { return webSearchEnabled },
		},
	}

	metas, plan, resolvedSurfaceID, err := promptloop.ProjectTurnTools(ctx, pDeps, sess, history, frame)
	if err != nil {
		return prompts.Reader{}, nil, err
	}

	offered := make([]string, 0, len(metas))
	projectedSchemas := make(map[string]map[string]any, len(metas))
	loadedMap := maps.Clone(e.loadedTools(sess))
	for _, meta := range metas {
		offered = append(offered, meta.Name)
		if meta.ArgsSchema != nil {
			projectedSchemas[meta.Name] = meta.ArgsSchema
		}
		if resolvedSurfaceID == "" {
			if loadedMap == nil {
				loadedMap = map[string]bool{}
			}
			loadedMap[meta.Name] = true
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

	var spawnAllowlist []string
	if coordinator {
		switch {
		case frame.Roster != nil:
			spawnAllowlist = frame.Roster.Effective
		case len(frame.RunContext.AllowedAgents) > 0:
			spawnAllowlist = frame.RunContext.AllowedAgents
		default:
			spawnAllowlist = spawn.AmbientAllowedAgents()
		}
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

	mode := ""
	if resolvedSurfaceID != "" {
		mode = surface.ExecutionModeFamily(resolvedSurfaceID)
	}
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID:             profileID,
		SurfaceID:             resolvedSurfaceID,
		Mode:                  mode,
		SessionMode:           sessionMode,
		Profiles:              profiles,
		Plan:                  plan,
		Loaded:                loadedMap,
		Visible:               offered,
		ProjectedSchemas:      projectedSchemas,
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
		return prompts.Reader{}, nil, err
	}
	visionEnabled := e.deps.ModelVision != nil && e.deps.ModelVision(ctx, sess)
	return reader.WithVision(visionEnabled), projectedSchemas["wait"], nil
}
