package turnnudges

import (
	"context"
	"maps"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolscope"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/pkg/api"
)

type Surface interface{ PromptTurnSurfaceID(string) string }

// TurnToolProjector projects callable tool schemas, the compiled surface plan,
// and the active coordinator surface ID through the live PromptLoop pipeline.
type TurnToolProjector interface {
	ProjectTurnTools(ctx context.Context, sess *api.Session, history []api.Message, frame inject.CoordinatorTurnFrame) ([]tools.ToolMeta, toolsurface.Plan, string, error)
}

type Spend interface {
	ClaimWarning(context.Context, *api.Session, float64) bool
}

// Profiles resolves session-constrained tool profiles and profile IDs for turn nudges.
type Profiles interface {
	ForSession(context.Context, *api.Session) []sandbox.ToolProfile
	PromptToolProfile(context.Context, *api.Session) (string, error)
	CommandExternalHidden() bool
}

// Workspace resolves attached workspace root paths for a session.
type Workspace interface {
	Paths(context.Context, *api.Session) ([]string, error)
}

type Service struct {
	ledger        *turnload.Ledger
	Spend         Spend
	surface       Surface
	profiles      Profiles
	workspace     Workspace
	prompts       prompts.PromptTemplateEngine
	webSearch     func() bool
	workerBranch  func(context.Context, *api.Session) (string, bool)
	promptSurface func(context.Context, *api.Session) prompts.AgentPromptSurface
	history       func(context.Context, string) ([]api.Message, error)
}

func New(ledger *turnload.Ledger, spend Spend) *Service {
	return &Service{ledger: ledger, Spend: spend}
}
func (m *Service) SetLedger(ledger *turnload.Ledger)               { m.ledger = ledger }
func (m *Service) SetSurface(surface Surface)                      { m.surface = surface }
func (m *Service) SetProfiles(profiles Profiles)                   { m.profiles = profiles }
func (m *Service) SetWorkspace(workspace Workspace)                { m.workspace = workspace }
func (m *Service) SetPrompts(prompts prompts.PromptTemplateEngine) { m.prompts = prompts }
func (m *Service) SetWebSearch(fn func() bool)                     { m.webSearch = fn }
func (m *Service) SetWorkerBranch(fn func(context.Context, *api.Session) (string, bool)) {
	m.workerBranch = fn
}
func (m *Service) SetPromptSurface(fn func(context.Context, *api.Session) prompts.AgentPromptSurface) {
	m.promptSurface = fn
}
func (m *Service) SetHistory(fn func(context.Context, string) ([]api.Message, error)) {
	m.history = fn
}

func (m *Service) renderKick(ctx context.Context, reader prompts.Reader, id string, data map[string]any) (string, error) {
	if m == nil || m.prompts == nil {
		return "", nil
	}
	return m.prompts.RenderKick(ctx, reader, strings.TrimSpace(id), data)
}

type sessionHostFacts struct {
	rootCount             int
	hasGit                bool
	hasAnyGitRoot         bool
	onWorkerBranch        bool
	webSearch             bool
	reviewsConfinedWrites bool
	externalHidden        bool
	scratchDir            string
	sharedTemp            []string
}

func (m *Service) resolveHostFacts(ctx context.Context, sess *api.Session) sessionHostFacts {
	facts := sessionHostFacts{webSearch: true}
	if m != nil && m.webSearch != nil {
		facts.webSearch = m.webSearch()
	}
	if m != nil && m.profiles != nil {
		facts.externalHidden = m.profiles.CommandExternalHidden()
	}
	var paths []string
	workspaceResolved := false
	if m != nil && m.workspace != nil && sess != nil {
		if resolved, err := m.workspace.Paths(ctx, sess); err == nil {
			paths = resolved
			facts.rootCount = len(resolved)
			workspaceResolved = true
		}
	}
	if !workspaceResolved {
		if sess != nil && strings.TrimSpace(sess.WorkspacePath) != "" {
			paths = []string{strings.TrimSpace(sess.WorkspacePath)}
			facts.rootCount = 1
		} else if m == nil || m.workspace == nil {
			facts.rootCount = 1
		}
	}
	for i, raw := range paths {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if _, ok := gitrepo.Discover(p); ok {
			if i == 0 {
				facts.hasGit = true
			}
			facts.hasAnyGitRoot = true
		}
	}
	if m != nil && m.workerBranch != nil && sess != nil {
		branchRoot, onBranch := m.workerBranch(ctx, sess)
		facts.onWorkerBranch = onBranch
		if !facts.hasGit && strings.TrimSpace(branchRoot) != "" {
			if _, ok := gitrepo.Discover(strings.TrimSpace(branchRoot)); ok {
				facts.hasGit = true
				facts.hasAnyGitRoot = true
			}
		}
	}
	if m != nil && m.promptSurface != nil && sess != nil {
		surf := m.promptSurface(ctx, sess)
		if surf.Posture != "" {
			facts.reviewsConfinedWrites = surf.Posture.ReviewsConfinedWrites()
		}
		if surf.CommandExternalHidden {
			facts.externalHidden = true
		}
		facts.scratchDir = surf.SessionScratchDir
		facts.sharedTemp = surf.SharedTempLocations
	}
	return facts
}

func (m *Service) sessionProfiles(ctx context.Context, sess *api.Session) []sandbox.ToolProfile {
	if m != nil && m.profiles != nil {
		if list := m.profiles.ForSession(ctx, sess); len(list) > 0 {
			return list
		}
	}
	list, _ := sandbox.LoadToolProfiles()
	return list
}

func (m *Service) resolveProfileID(ctx context.Context, sess *api.Session, explicit string) string {
	if id := strings.TrimSpace(explicit); id != "" {
		if list := m.sessionProfiles(ctx, sess); hasProfile(list, id) {
			return id
		}
		if resolved, err := prompts.ToolProfileForAgent(id); err == nil {
			return resolved
		}
	}
	if m != nil && m.profiles != nil && sess != nil {
		if id, err := m.profiles.PromptToolProfile(ctx, sess); err == nil && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	if sess != nil && strings.TrimSpace(sess.AgentType) != "" {
		if resolved, err := prompts.ToolProfileForAgent(sess.AgentType); err == nil {
			return resolved
		}
	}
	return prompts.CoordinatorProfileID
}

func hasProfile(profiles []sandbox.ToolProfile, id string) bool {
	for _, p := range profiles {
		if p.ID == id {
			return true
		}
	}
	return false
}

func projectedTurnReader(
	ctx context.Context,
	projector TurnToolProjector,
	sess *api.Session,
	profileID, sessionMode string,
	profiles []sandbox.ToolProfile,
	loaded map[string]bool,
	history []api.Message,
	facts sessionHostFacts,
) prompts.Reader {
	var frame inject.CoordinatorTurnFrame
	if sess.IsWorkerChild() || profileID != prompts.CoordinatorProfileID {
		frame.Machine = inject.Machine{ProfileID: profileID, SkillCount: 1}
	}
	metas, plan, resolvedSurfaceID, err := projector.ProjectTurnTools(ctx, sess, history, frame)
	if err != nil {
		return prompts.Reader{}
	}
	if !sess.IsWorkerChild() && profileID == prompts.CoordinatorProfileID && strings.TrimSpace(resolvedSurfaceID) == "" {
		return prompts.Reader{}
	}
	offered := make([]string, 0, len(metas))
	projectedSchemas := make(map[string]map[string]any, len(metas))
	loadedMap := maps.Clone(loaded)
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
	mode := ""
	if resolvedSurfaceID != "" {
		mode = surface.ExecutionModeFamily(resolvedSurfaceID)
	}
	r, rErr := prompts.NewReader(prompts.ReaderInput{
		ProfileID:             profileID,
		SurfaceID:             resolvedSurfaceID,
		Mode:                  mode,
		SessionMode:           sessionMode,
		Profiles:              profiles,
		Plan:                  plan,
		Loaded:                loadedMap,
		Visible:               offered,
		ProjectedSchemas:      projectedSchemas,
		RootCount:             facts.rootCount,
		HasGit:                facts.hasGit,
		HasAnyGitRoot:         facts.hasAnyGitRoot,
		OnWorkerBranch:        facts.onWorkerBranch,
		WebSearch:             facts.webSearch,
		ReviewsConfinedWrites: facts.reviewsConfinedWrites,
		CommandExternalHidden: facts.externalHidden,
		SessionScratchDir:     facts.scratchDir,
		SharedTempLocations:   facts.sharedTemp,
	})
	if rErr != nil {
		return prompts.Reader{}
	}
	return r
}

func (m *Service) compiledCoordinatorSurfaceReader(
	sess *api.Session,
	sessionMode string,
	profiles []sandbox.ToolProfile,
	loaded map[string]bool,
	history []api.Message,
	facts sessionHostFacts,
) prompts.Reader {
	surfaceID := ""
	if m.surface != nil {
		surfaceID = strings.TrimSpace(m.surface.PromptTurnSurfaceID(sess.ID))
	}
	if surfaceID == "" {
		surfaceID = surface.ResolveTurnProfile(api.CoordinatorRunContext{}, sess, history).SurfaceID
	}
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID, SessionMode: sess.Mode}, facts.rootCount)
	if err != nil {
		return prompts.Reader{}
	}
	if !facts.webSearch {
		plan = plan.Without(webresearch.SearchToolName, webresearch.FetchURLToolName)
	}
	if facts.rootCount == 0 {
		for _, name := range plan.AddressableNames() {
			if toolscope.RequiresProjectRoots(name) {
				plan = plan.Without(name)
			}
		}
	}
	if sess.Mode == api.SessionModeAsk {
		for _, name := range plan.AddressableNames() {
			if !tools.AllowedInAsk(tools.ToolMeta{Name: name}) {
				plan = plan.Without(name)
			}
		}
		plan = plan.Without("ask_user")
	}
	r, rErr := prompts.NewReader(prompts.ReaderInput{
		ProfileID:             prompts.CoordinatorProfileID,
		SurfaceID:             surfaceID,
		Mode:                  surface.ExecutionModeFamily(surfaceID),
		SessionMode:           sessionMode,
		Profiles:              profiles,
		Plan:                  plan,
		Loaded:                loaded,
		RootCount:             facts.rootCount,
		HasGit:                facts.hasGit,
		HasAnyGitRoot:         facts.hasAnyGitRoot,
		OnWorkerBranch:        facts.onWorkerBranch,
		WebSearch:             facts.webSearch,
		ReviewsConfinedWrites: facts.reviewsConfinedWrites,
		CommandExternalHidden: facts.externalHidden,
		SessionScratchDir:     facts.scratchDir,
		SharedTempLocations:   facts.sharedTemp,
	})
	if rErr != nil {
		return prompts.Reader{}
	}
	return r
}

// activeTurnReader constructs the Reader for the session's current tool-bearing turn.
func (m *Service) activeTurnReader(ctx context.Context, sess *api.Session, explicitProfileID string) prompts.Reader {
	if m == nil || sess == nil {
		return prompts.Reader{}
	}
	profiles := m.sessionProfiles(ctx, sess)
	if len(profiles) == 0 {
		return prompts.Reader{}
	}
	profileID := m.resolveProfileID(ctx, sess, explicitProfileID)
	facts := m.resolveHostFacts(ctx, sess)
	sessionMode := string(api.SessionModeAgent)
	if sess.Mode == api.SessionModeAsk {
		sessionMode = string(api.SessionModeAsk)
	}
	var loaded map[string]bool
	if m.ledger != nil {
		loaded = m.ledger.Active(sess.ID)
	}
	var history []api.Message
	if m.history != nil {
		history, _ = m.history(ctx, sess.ID)
	}

	if projector, ok := m.surface.(TurnToolProjector); ok {
		return projectedTurnReader(ctx, projector, sess, profileID, sessionMode, profiles, loaded, history, facts)
	}
	if !sess.IsWorkerChild() && profileID == prompts.CoordinatorProfileID {
		return m.compiledCoordinatorSurfaceReader(sess, sessionMode, profiles, loaded, history, facts)
	}

	var visible []string
	for _, p := range profiles {
		if p.ID != profileID {
			continue
		}
		visible = make([]string, 0, len(p.Tools))
		for name := range p.Tools {
			if !p.ToolAllowed(name) {
				continue
			}
			if !toolcontract.AdmitsSession(name, sess.IsWorkerChild()) {
				continue
			}
			if !facts.webSearch && (name == webresearch.SearchToolName || name == webresearch.FetchURLToolName) {
				continue
			}
			if facts.rootCount == 0 && toolscope.RequiresProjectRoots(name) {
				continue
			}
			if sess.Mode == api.SessionModeAsk && !tools.AllowedInAsk(tools.ToolMeta{Name: name}) {
				continue
			}
			visible = append(visible, name)
		}
		break
	}
	r, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID:             profileID,
		SessionMode:           sessionMode,
		Profiles:              profiles,
		Loaded:                loaded,
		Visible:               visible,
		RootCount:             facts.rootCount,
		HasGit:                facts.hasGit,
		HasAnyGitRoot:         facts.hasAnyGitRoot,
		OnWorkerBranch:        facts.onWorkerBranch,
		WebSearch:             facts.webSearch,
		ReviewsConfinedWrites: facts.reviewsConfinedWrites,
		CommandExternalHidden: facts.externalHidden,
		SessionScratchDir:     facts.scratchDir,
		SharedTempLocations:   facts.sharedTemp,
	})
	if err != nil {
		return prompts.Reader{}
	}
	return r
}

// commandMutationCapable reports whether the session/leg can emit mutating
// command redirects on tool-bearing turns in its current mode, posture, and roots.
func (m *Service) commandMutationCapable(ctx context.Context, sess *api.Session, explicitProfileID string) bool {
	if m == nil || sess == nil || sess.Mode == api.SessionModeAsk {
		return false
	}
	r := m.activeTurnReader(ctx, sess, explicitProfileID)
	return r.Valid() && r.Held()[toolcontract.ReachCommandMutation]
}
