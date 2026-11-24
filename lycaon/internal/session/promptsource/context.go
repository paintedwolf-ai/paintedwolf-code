package promptsource

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	sessionlimits "github.com/lycaon/lycaon/internal/session/limits"
	"github.com/lycaon/lycaon/internal/session/loading"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/session/policyindex"
	"github.com/lycaon/lycaon/internal/session/processcontrol"
	"github.com/lycaon/lycaon/internal/session/profiles"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/toolcontext"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/session/turnguards"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/trainingcapture"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(ctx context.Context, id string) (*api.Session, error)
	ResolveEffectiveMode(context.Context, string) (store.RootAuthority, error)
}

type Transcript interface {
	AppendDraftVersion(ctx context.Context, sessionID, slotID, body, outcomeCode string) (int, error)
	CountDraftVersions(ctx context.Context, sessionID, slotID string) (int, error)
	GetMessages(ctx context.Context, id string) ([]api.Message, error)
}

type Turns interface {
	AdmitModelResponse(ctx context.Context, sessionID, attemptID string) error
	CheckpointTurn(ctx context.Context, turnID, attemptID string, phase store.TurnPhase, checkpointJSON string) error
	MarkModelOutputProjected(ctx context.Context, outputID string) error
	SettleModelOutput(ctx context.Context, out store.ModelOutput) (store.ModelOutput, error)
	SettledModelOutputIteration(ctx context.Context, attemptID string) (int, error)
	TouchTurnProgress(ctx context.Context, turnID string) error
}

type Evidence interface {
	CommitEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content string) (string, string, error)
	CommitProgramHTTPProvenance(ctx context.Context, target evidence.ProgramProvenanceTarget, prov evidence.ProgramHTTPProvenance) (string, error)
	CommitProgramObservation(ctx context.Context, sessionID, tool string, receipt *api.CommandProgramReceipt, content string) (string, error)
	CommitVisualEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content, artifactID string) (string, string, error)
}
type Context struct {
	ActivateAgentWorkflow func(context.Context, string) error
	PolicyIndex           *policyindex.Service
	Frame                 inject.CoordinatorTurnFrameSource
	Guards                *turnguards.Service
	Limits                *sessionlimits.Service
	Loading               *loading.Service
	Pages                 *pagesession.Registry
	Policy                *policyfacts.Service
	Processes             *processcontrol.Service
	Profiles              *profiles.Service
	Prompts               prompts.PromptTemplateEngine
	Runtime               *coordinator.Runtime
	Sessions              Sessions
	ToolContext           *toolcontext.Service
	Tools                 tools.ToolRegistry
	WebResearch           *webresearch.ConfigStore
	WorkerState           *workeroutcomes.State
	Workspace             *sessionscope.Service
	Workspaces            *workerworkspace.Service
}

func (m *Context) Build() promptloop.ContextDeps {
	rt := m.Runtime
	deps := promptloop.ContextDeps{
		Limits: m.Limits.Effective,
		Tools:  m.Tools,
		ResolveEffectiveMode: func(ctx context.Context, id string) (store.RootAuthority, error) {
			if m.Sessions == nil {
				return store.RootAuthority{}, store.ErrSessionModeUnavailable
			}
			return m.Sessions.ResolveEffectiveMode(ctx, id)
		},
		ActivateAgentWorkflow: m.ActivateAgentWorkflow,
		RefreshRoundMachine:   m.Profiles.CompileMachine,
		RootSessionID: func(ctx context.Context, sessionID string) string {
			return sessiontree.RootID(ctx, m.Sessions, sessionID)
		},
		Policy: m.Guards.Policy(),
		ProjectRootCount: func(ctx context.Context, sess *api.Session) int {
			_, count, _, _ := m.Workspace.PromptRootRows(ctx, sess)
			return count
		},
		WritableSharedTemp: m.Profiles.WritableSharedTemp,
		LoadedTools:        m.Loading.LoadedTools,
		LiveResources: func(sessionID string) toolcontract.ResourcePresence {
			presence := toolcontract.ResourcePresence{}
			if m != nil && m.Processes.Background != nil {
				presence.CommandJobs = m.Processes.Background.HasPipelineHandles(sessionID)
				presence.Terminals = m.Processes.Background.HasPTYHandles(sessionID)
			}
			if m != nil && m.Pages != nil {
				presence.Pages = m.Pages.CountLive(sessionID) > 0
			}
			if m != nil {
				presence.HeldCalls = m.Processes.Held.HasHandles(sessionID)
			}
			return presence
		},
		BuildMessages:         m.Runtime.BuildCompletionMessages,
		ImplementSessionState: m.WorkerState.ForSession,
		ContextSurfaceDeps: promptloop.ContextSurfaceDeps{
			PrepareAgentPolicy:              m.PolicyIndex.PrepareBinding,
			CoordinatorSurfaceActivityLabel: surface.LoadCoordinatorSurfaceActivityLabel,
			SetPromptTurnSurface: func(sessionID, surfaceID string) {
				rt.Assembly().SetTurnSurfaceID(sessionID, surfaceID)
			},
			PromptTurnSurface: func(sessionID string) string {
				return rt.PromptTurnSurfaceID(sessionID)
			},
			CoordinatorPostureRules: func(ctx context.Context, sess *api.Session) ([]string, error) {
				if sess == nil || sess.Posture == "" {
					return nil, nil
				}
				postures, err := m.Profiles.EffectivePostures(ctx, sess)
				if err != nil || postures == nil {
					return nil, err
				}
				paths, err := postures.RulesPaths(sess.Posture)
				if err != nil {
					return nil, err
				}
				return append([]string(nil), paths...), nil
			},
			OverlayRootPaths: func(ctx context.Context, sess *api.Session) []string {
				return m.Workspace.SettingsRoots(ctx, sess)
			},
			ToolObserved:     m.Loading.ObserveToolCall,
			WebSearchEnabled: m.WebSearchEnabled,
			CoordinatorFrame: m.Frame,
		},
	}
	deps.RefreshToolContext = func(ctx context.Context, sess *api.Session, machine inject.Machine) (tools.ToolContext, error) {
		profileID := strings.TrimSpace(machine.ProfileID)
		if profileID == "" {
			var err error
			profileID, err = m.Profiles.PromptToolProfile(ctx, sess)
			if err != nil {
				return tools.ToolContext{}, err
			}
		}
		tctx, err := m.ToolContext.Build(ctx, sess, profileID, machine)
		if err != nil {
			return tools.ToolContext{}, err
		}
		return m.Workspaces.Enrich(ctx, sess, tctx)
	}
	deps.MCPAlwaysLoad = func(ctx context.Context, sess *api.Session) map[string]bool {
		if m.Policy.MCP == nil {
			return nil
		}
		return m.Policy.MCP.ToolLoadingModes(ctx, m.Workspace.SettingsPath(ctx, sess))
	}

	if m.Prompts != nil {
		deps.ToolProcedures = m.ToolProcedures
		deps.PromptSurface = m.PromptSurface
	}

	return deps
}

func (m *Context) ToolProcedures(ctx context.Context, sess *api.Session, profileID string, offered []string) (string, error) {
	reader, err := m.procedureReader(ctx, sess, profileID, offered)
	if err != nil {
		return "", err
	}
	return inject.RenderToolProceduresBlock(ctx, prompts.NewInjectRenderer(m.Prompts), sess.ID, reader)
}

func (m *Context) procedureReader(ctx context.Context, sess *api.Session, profileID string, offered []string) (prompts.Reader, error) {
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		return prompts.Reader{}, err
	}
	if m != nil && m.Profiles != nil && sess != nil {
		profiles = m.Profiles.ConstrainCommandProfiles(ctx, sess.ID, profiles)
	}
	surfaceID := ""
	resolvedProfileID := strings.TrimSpace(profileID)
	var deferred []string
	if prompts.IsCoordinatorSurfaceID(resolvedProfileID) {
		surfaceID = resolvedProfileID
		resolvedProfileID = prompts.CoordinatorProfileID
		deferred, _ = prompts.LoadCoordinatorSurfaceLoadable(surfaceID)
	} else {
		if mapped, err := prompts.ToolProfileForAgent(resolvedProfileID); err == nil && mapped != "" {
			resolvedProfileID = mapped
		}
		for _, p := range profiles {
			if p.ID == resolvedProfileID {
				for name, enabled := range p.Tools {
					if enabled && p.ToolAllowed(name) && !strings.HasSuffix(name, "*") {
						deferred = append(deferred, name)
					}
				}
				break
			}
		}
	}
	offeredSet := make(map[string]bool, len(offered))
	for _, name := range offered {
		offeredSet[name] = true
	}
	var remaining []string
	if offeredSet["request_tools"] {
		for _, name := range deferred {
			if sess != nil && sess.Mode == api.SessionModeAsk && !tools.AllowedInAsk(tools.ToolMeta{Name: name}) {
				continue
			}
			if !offeredSet[name] {
				remaining = append(remaining, name)
			}
		}
	}
	visible := slices.Clone(offered)
	if visible == nil {
		visible = []string{}
	}
	if surfaceID == "" {
		visible = append(visible, remaining...)
	}
	var (
		rootRows   []map[string]any
		activePath string
	)
	rootCount := 0
	if m != nil && m.Workspace != nil && sess != nil {
		rootRows, rootCount, activePath, _ = m.Workspace.PromptRootRows(ctx, sess)
		if rootCount < 0 {
			rootCount = 0
		}
	} else if sess != nil && strings.TrimSpace(sess.WorkspacePath) != "" {
		rootCount = 1
	}
	sessionMode := string(api.SessionModeAgent)
	var fallbackPath string
	if sess != nil {
		if sess.Mode == api.SessionModeAsk {
			sessionMode = string(api.SessionModeAsk)
		}
		fallbackPath = sess.WorkspacePath
	}
	var (
		branchRoot     string
		onWorkerBranch bool
	)
	if m != nil && m.Workspaces != nil {
		branchRoot, onWorkerBranch = m.Workspaces.OnWorkerBranch(ctx, sess)
	}
	hasGit, hasAnyGitRoot := procedureGitFacts(rootRows, activePath, fallbackPath, branchRoot)
	var (
		reviewsConfinedWrites bool
		externalHidden        bool
		scratchDir            string
		sharedTemp            []string
		spawnAllowlist        []string
	)
	if m != nil && m.Profiles != nil {
		externalHidden = m.Profiles.CommandExternalHidden()
		if sess != nil {
			reviewsConfinedWrites = m.Profiles.ProjectApprovalPosture(ctx, sess).ReviewsConfinedWrites()
			sharedTemp = m.Profiles.WritableSharedTemp(ctx, sess)
		}
	}
	if m != nil && m.ToolContext != nil && sess != nil {
		scratchDir = m.ToolContext.ScratchDir(ctx, sess)
	}
	if resolvedProfileID == prompts.CoordinatorProfileID {
		spawnAllowlist = spawn.AmbientAllowedAgents()
		if m != nil && m.Frame != nil && sess != nil {
			if frame, frameErr := m.Frame.BuildCoordinatorTurnFrame(ctx, sess.ID, sess); frameErr == nil && len(frame.RunContext.AllowedAgents) > 0 {
				spawnAllowlist = frame.RunContext.AllowedAgents
			}
		}
	}
	return prompts.NewReader(prompts.ReaderInput{
		ProfileID:             resolvedProfileID,
		SurfaceID:             surfaceID,
		SessionMode:           sessionMode,
		Profiles:              profiles,
		Plan:                  toolsurface.Compile(offered, remaining),
		Loaded:                offeredSet,
		Visible:               visible,
		ProjectedSchemas:      prompts.ProjectedSchemasFromContext(ctx),
		RootCount:             rootCount,
		HasGit:                hasGit,
		HasAnyGitRoot:         hasAnyGitRoot,
		OnWorkerBranch:        onWorkerBranch,
		WebSearch:             m.WebSearchEnabled(),
		SpawnAllowlist:        spawnAllowlist,
		ReviewsConfinedWrites: reviewsConfinedWrites,
		CommandExternalHidden: externalHidden,
		SessionScratchDir:     scratchDir,
		SharedTempLocations:   sharedTemp,
	})
}

func procedureGitFacts(roots []map[string]any, activePath, fallbackWorkspacePath, workerBranchRoot string) (hasGit, hasAnyGitRoot bool) {
	primaryPath := strings.TrimSpace(activePath)
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

// PromptSurface reports the host facts that shaped a request offering these
// tools: their turn-load sources, the procedure units they attached, the skill
// the turn read and the decision in effect.
func (m *Context) PromptSurface(ctx context.Context, sess *api.Session, profileID string, offered []string) (trainingcapture.PromptSurface, error) {
	surface := trainingcapture.PromptSurface{Tools: []trainingcapture.OfferedTool{}, Units: []trainingcapture.RenderedUnit{}}
	var standing turnload.Standing
	if m.Loading != nil && m.Loading.Ledger != nil {
		standing = m.Loading.Ledger.Standing(sess.ID)
	}
	loads := make(map[string]turnload.Load, len(standing.Tools))
	for _, load := range standing.Tools {
		loads[load.Tool] = load
	}
	offeredSet := make(map[string]bool, len(offered))
	for _, name := range offered {
		offeredSet[name] = true
		tool := trainingcapture.OfferedTool{Tool: name, Source: "standing"}
		if load, ok := loads[name]; ok {
			tool.Source, tool.With = string(load.Source), load.With
		}
		surface.Tools = append(surface.Tools, tool)
	}
	reader, err := m.procedureReader(ctx, sess, profileID, offered)
	if err != nil {
		return surface, err
	}
	units, err := inject.ToolProcedureUnits(prompts.NewInjectRenderer(m.Prompts), reader)
	if err != nil {
		return surface, err
	}
	for _, unit := range units {
		rendered := trainingcapture.RenderedUnit{ID: unit.ID}
		for _, tool := range unit.Attaches {
			if offeredSet[tool] {
				rendered.AttachedBy = append(rendered.AttachedBy, tool)
			}
		}
		surface.Units = append(surface.Units, rendered)
	}
	if standing.Skill != nil {
		surface.Skill = &trainingcapture.SkillRead{Name: standing.Skill.Name, Tool: standing.Skill.Tool}
	}
	receipt, found, err := m.Loading.LatestDecision(ctx, sess.ID)
	if err != nil {
		return surface, err
	}
	if found {
		surface.TurnLoadReceiptID, surface.Engine, surface.CatalogRevision = receipt.ID, receipt.Engine, receipt.CatalogRevision
	}
	return surface, nil
}

func (m *Context) WebSearchEnabled() bool {
	return m.WebResearch == nil || m.WebResearch.SearchEnabled()
}

func (m *Context) RunContext(ctx context.Context, sessionID string) (api.CoordinatorRunContext, error) {
	if m == nil || m.Sessions == nil {
		return api.CoordinatorRunContext{}, fmt.Errorf("session store not configured")
	}
	sess, err := m.Sessions.Get(ctx, sessionID)
	if err != nil {
		return api.CoordinatorRunContext{}, err
	}
	if m.Frame == nil {
		return api.CoordinatorRunContext{}, nil
	}
	frame, err := m.Frame.BuildCoordinatorTurnFrame(ctx, sessionID, sess)
	if err != nil {
		return api.CoordinatorRunContext{}, err
	}
	runCtx := frame.RunContext
	state := m.WorkerState.ForSession(ctx, sess)
	if wirePhase := batch.ToWirePhase(state.BatchPhase); wirePhase != "" {
		runCtx.BatchPhase = wirePhase
		runCtx.BatchSeq = state.BatchSeq
	}
	return runCtx, nil
}
