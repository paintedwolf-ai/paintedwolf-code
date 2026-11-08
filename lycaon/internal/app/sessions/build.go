package sessions

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/app/configuration"
	"github.com/lycaon/lycaon/internal/app/execution"
	"github.com/lycaon/lycaon/internal/app/persistence"
	"github.com/lycaon/lycaon/internal/app/providers"
	"github.com/lycaon/lycaon/internal/app/security"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/harnessfixture"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
)

// Dependencies defines the required inputs to build the session manager.
type Dependencies struct {
	Storage           persistence.Runtime
	Catalog           configuration.Catalog
	Providers         providers.Runtime
	Execution         execution.Runtime
	Security          *security.Runtime
	Settings          configuration.Runtime
	Agents            configuration.Agents
	TestSessionLimits *settings.SessionLimits
	RegisterRecovery  func(bootrecovery.Entry) error
	Resources         ResourceTracker
}

// Build creates the session manager, prompt engine, and crash recovery handlers.
func Build(ctx context.Context, deps Dependencies) (*Runtime, error) {
	if configdir.IsHarnessChannel() && deps.Providers.Service != nil {
		preparation, err := harnessfixture.NewPreludeController(deps.Storage.Directory, deps.Storage.Sessions)
		if err != nil {
			return nil, err
		}
		deps.Providers.Service.Preparation = preparation
	}

	mgr := session.NewHost(deps.Storage.Sessions, session.Models{Client: deps.Providers.Client, Provider: deps.Providers.Service, Limits: deps.Settings.SessionLimits, Cost: deps.Providers.Costs}, deps.Execution.Registry)
	if deps.Resources != nil {
		deps.Resources.Track("session-host", 21, func(c context.Context) error {
			mgr.BeginEngineShutdown()
			return mgr.WaitForEngineShutdown(c)
		})
	}
	deps.Security.BindRemember(mgr.ToolPolicy.SetRememberSecrets)
	if err := deps.Execution.TurnSources.Bind(mgr.Coordinator.Loading); err != nil {
		return nil, err
	}
	mgr.ToolPolicy.SetMintedCredentialSource(deps.Security.Detections.MintedCredentialSource)
	invocations := invocation.NewSQLRecorder(deps.Storage.Database)
	mgr.SetInvocationRecorder(invocations)

	if deps.RegisterRecovery != nil {
		if err := deps.RegisterRecovery(bootrecovery.Entry{
			Name: "tool-invocations", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
			Run: func(c context.Context) error {
				_, err := invocations.InterruptRunning(c)
				return err
			},
		}); err != nil {
			return nil, err
		}
		if err := deps.RegisterRecovery(bootrecovery.Entry{
			Name: "session-turns", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseBuild,
			After: []string{"tool-invocations"},
			Run:   mgr.Stops.Recovery.RecoverOrphanedTurns,
		}); err != nil {
			return nil, err
		}
		if err := deps.RegisterRecovery(bootrecovery.Entry{
			Name: "transcript-invocations", Kind: bootrecovery.KindReconcile, Phase: bootrecovery.PhaseServe,
			After: []string{"tool-invocations", "session-turns"},
			Run:   mgr.Stops.Recovery.RecoverInterruptedToolResults,
		}); err != nil {
			return nil, err
		}
	}

	ledger := deps.Storage.SourceLedger
	if deps.RegisterRecovery != nil {
		if err := deps.RegisterRecovery(bootrecovery.Entry{
			Name: "source-command-windows", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
			Run: ledger.Commands.RecoverInterruptedWindows,
		}); err != nil {
			return nil, err
		}
	}
	mgr.SetSourceLedger(ledger, tools.SourceHistory{
		Files: ledger.History, Comparison: ledger.Comparisons, Git: ledger.Git, Authorship: ledger.Walk, Windows: ledger.Commands, Tree: session.OverlayTree{},
	}, ledger.Commands, ledger.Git, ledger.Checkpoints, ledger.Inventory)
	mgr.Profiles.SetAgentRegistry(deps.Agents.Registry)
	mgr.Profiles.SetHostResources(deps.Settings.HostResources)
	if deps.Settings.HostResources != nil && deps.Settings.Service != nil && deps.Settings.Service.Approvals != nil {
		deps.Settings.HostResources.SetPolicyBinder(configuration.HostResourcePolicyBinder(deps.Settings.Service.Approvals, &mgr.Catalog))
	}

	promptLayers := prompts.PromptLayers{
		ModuleRoot: deps.Catalog.ModuleRoot,
		Site:       prompts.SitePromptFilesDir(deps.Catalog.ModuleRoot),
	}
	promptEngine := prompts.NewFileTemplateEngineLayers(promptLayers)
	mgr.SetPromptEngine(promptEngine)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(promptEngine))
	mgr.Profiles.SetPostureRegistry(deps.Agents.Postures)
	mgr.SetProjectRegistry(deps.Storage.Projects)
	mgr.SetDataDir(deps.Storage.Directory)
	mgr.SetScratchFolders(scratch.New(deps.Storage.Directory))
	if deps.Storage.Sessions != nil {
		deps.Storage.Sessions.SetDataDir(deps.Storage.Directory)
	}
	mgr.SetDoomLoopGuard(loopguard.NewMemoryDoomLoopGuard())
	mgr.SetRejectFormatter(deps.Execution.Rejections)
	mgr.Coordinator.Guards.SetRuntimeRules(loadProfileRuntimeRules())
	if err := progress.InitProgressGatedTools(deps.Catalog.ModuleRoot); err != nil {
		return nil, fmt.Errorf("init progress-gated tools: %w", err)
	}
	mgr.Coordinator.Guards.SetToolMetadata(deps.Execution.Host.Executor.Metadata)
	mgr.Profiles.SetCommandExternalHidden(deps.Execution.Host.Executor.Metadata.CommandExternalHidden)
	if deps.Settings.Service != nil {
		if deps.TestSessionLimits == nil {
			mgr.Limits.SetProvider(settings.ProjectLimitsAdapter{Store: deps.Settings.Service.Limits})
		}
		mgr.SetEffectiveCatalogDeps(deps.Catalog.ModuleRoot, deps.Catalog.Effective, deps.Settings.Service.TrustSurfaces)
		mgr.Profiles.SetSkillsGate(deps.Settings.ProjectSurfaceGate(projectcontrib.SurfaceSkills, deps.Storage.Projects))
		mgr.Profiles.SetApprovals(deps.Settings.Service.Approvals)
	}
	if deps.Catalog.ViewCache != nil {
		mgr.Catalog.SetCatalogViewCache(deps.Catalog.ViewCache)
	}

	workerToolBudgetFor := func(projectDir string) spawn.WorkerToolBudget {
		if deps.Settings.Service == nil {
			return deps.Settings.SessionLimits.WorkerToolBudget()
		}
		if !deps.Settings.ProjectSurfaceGate(projectcontrib.SurfaceProjectSettings, deps.Storage.Projects).AppliesPath(context.WithoutCancel(ctx), projectDir) {
			projectDir = ""
		}
		return settings.ProjectLimitsAdapter{Store: deps.Settings.Service.Limits}.SessionLimits(projectDir).WorkerToolBudget()
	}

	wireSessionToolSources(mgr, deps.Execution.Host, deps.Settings.HostResources, workerToolBudgetFor)

	if err := deps.Security.BuildAuthorization(deps.Catalog.ModuleRoot, deps.Agents.ToolProfiles, deps.Settings.Service.Approvals, deps.Execution.Host.Authority.ApprovalGate, deps.Execution.Host.Registry.List, mgr.Profiles.ResolveToolAccess, workerToolBudgetFor); err != nil {
		return nil, err
	}
	mgr.Runner.Authorization.SetSealer(deps.Security.Authority.Sealer)
	if compactor, err := loadCompactor(deps.Providers.Service, deps.Providers.Costs); err == nil && compactor != nil {
		mgr.Runner.History.SetCompactor(compactor)
	}

	return &Runtime{
		Manager:             mgr,
		Store:               deps.Storage.Sessions,
		PromptEngine:        promptEngine,
		Invocations:         invocations,
		WorkerToolBudgetFor: workerToolBudgetFor,
	}, nil
}

// skillSurfaceVarsInput maps a tool invocation context to skill surface input:
// the agent's tool profile, the turn surface, the offered tools, and the
// workspace shape facts (worker branch, git, root count).
func skillSurfaceVarsInput(tctx tools.ToolContext, budget spawn.WorkerToolBudget) prompts.SkillSurfaceVarsInput {
	profileID := strings.TrimSpace(tctx.Identity.Agent)
	if mapped, err := prompts.ToolProfileForAgent(profileID); err == nil && mapped != "" {
		profileID = mapped
	}
	return prompts.SkillSurfaceVarsInput{
		ProfileID:        profileID,
		SurfaceID:        strings.TrimSpace(tctx.Turn.TurnSurfaceID),
		Offered:          tctx.Turn.TurnOfferedToolNames,
		OnWorkerBranch:   tctx.Source.SourceWorkspaceKind == api.SourceWorkspaceKindWorker && strings.TrimSpace(tctx.Source.WorkerBranchRoot) != "",
		HasGit:           skillReaderHasGit(tctx),
		RootCount:        len(tctx.Source.Roots),
		DelegationBudget: budget,
	}
}

// skillReaderHasGit reports a usable git repository for the skill reader: the
// active root when known, else the worker branch root, else the first root.
func skillReaderHasGit(tctx tools.ToolContext) bool {
	for _, dir := range []string{strings.TrimSpace(tctx.ActiveRootPath()), strings.TrimSpace(tctx.Source.WorkerBranchRoot)} {
		if dir == "" {
			continue
		}
		if _, ok := gitrepo.Discover(dir); ok {
			return true
		}
	}
	for _, r := range tctx.Source.Roots {
		if _, ok := gitrepo.Discover(strings.TrimSpace(r.Path)); ok {
			return true
		}
	}
	return false
}

// skillReaderOfferedTools maps the tool facts a skill reader is checked
// against: the turn's offered tools, plus — on a coordinator surface — the
// surface's full addressable set (floor plus loadable), since request_tools
// can reach the rest of the surface before any skill read.
func skillReaderOfferedTools(tctx tools.ToolContext) map[string]bool {
	offered := make(map[string]bool, len(tctx.Turn.TurnOfferedToolNames)+1)
	for _, name := range tctx.Turn.TurnOfferedToolNames {
		offered[name] = true
	}
	profileID := profileIDForSurface(tctx)
	surfaceID := strings.TrimSpace(tctx.Turn.TurnSurfaceID)
	scopeID := profileID
	if surfaceID != "" && profileID == prompts.CoordinatorProfileID {
		scopeID = surfaceID
		if floor, err := prompts.LoadCoordinatorSurfaceFloor(surfaceID); err == nil {
			for _, name := range floor {
				offered[name] = true
			}
		}
		if loadable, err := prompts.LoadCoordinatorSurfaceLoadable(surfaceID); err == nil {
			for _, name := range loadable {
				offered[name] = true
			}
		}
	}
	if offered["command"] && prompts.ScopeAllowsCommandMutation(scopeID) {
		offered[toolvocab.ReachCommandMutation] = true
	}
	return offered
}

func profileIDForSurface(tctx tools.ToolContext) string {
	profileID := strings.TrimSpace(tctx.Identity.Agent)
	if mapped, err := prompts.ToolProfileForAgent(profileID); err == nil && mapped != "" {
		return mapped
	}
	return profileID
}

func wireSessionToolSources(mgr *session.Host, host *toolhost.Runtime, hostResources *hostresources.Service, workerToolBudgetFor func(string) spawn.WorkerToolBudget) {
	if host != nil && host.Boundary != nil {
		host.Boundary.SetProfileSource(func(ctx context.Context, sessionID string) []sandbox.ToolProfile {
			view := mgr.Catalog.ViewForSessionID(ctx, sessionID)
			if view == nil {
				return nil
			}
			return mgr.Profiles.ConstrainCommandProfiles(ctx, sessionID, view.ToolProfiles)
		})
		if host.Executor != nil {
			host.Executor.Metadata.SetToolSchemaSource(func(ctx context.Context, sessionID string) *toolschema.Config {
				view := mgr.Catalog.ViewForSessionID(ctx, sessionID)
				if view == nil {
					return nil
				}
				return view.ToolSchemas
			})
		}
	}
	if host != nil {
		host.Authority.SetApprovalRuleSource(&mgr.Catalog)
		if hostResources != nil {
			host.Executor.Network.SetHostResourceConnectionSource(hostResources.ResolveAction)
			host.Executor.Network.SetExecutableAlternatesSource(hostResources.ExecutableAlternates)
		}
		host.Skills.SetSkillsCatalog(func(ctx context.Context, tctx tools.ToolContext) []skills.Skill {
			roots := make([]string, 0, len(tctx.Source.Roots))
			for _, r := range tctx.Source.Roots {
				if path := strings.TrimSpace(r.Path); path != "" {
					roots = append(roots, path)
				}
			}
			sess, _ := mgr.Chats.Get(ctx, tctx.Identity.SessionID)
			loaded, _ := mgr.Profiles.EffectiveSkillsForProfile(ctx, sess, tctx.Identity.Agent, roots)
			offered := skillReaderOfferedTools(tctx)
			reachable := make([]skills.Skill, 0, len(loaded))
			for _, skill := range loaded {
				if toolvocab.SkillToolsOffered(skill, offered) {
					reachable = append(reachable, skill)
				}
			}
			return reachable
		})
		host.Skills.SetSkillTemplateVars(func(_ context.Context, tctx tools.ToolContext) map[string]any {
			budget := spawn.DefaultWorkerToolBudget()
			if workerToolBudgetFor != nil {
				budget = workerToolBudgetFor(strings.TrimSpace(tctx.ActiveRootPath()))
			}
			vars, err := prompts.SkillSurfaceTemplateVars(skillSurfaceVarsInput(tctx, budget))
			if err != nil || vars == nil {
				return spawn.PolicyTemplateVars(budget)
			}
			return vars
		})
		host.Skills.SetSkillPackConfiguration(
			func(ctx context.Context, tctx tools.ToolContext, packID string) map[string]any {
				view := mgr.Catalog.ViewForSessionID(ctx, tctx.Identity.SessionID)
				if view == nil {
					return nil
				}
				return view.Contributions.SettingsForPack(packID)
			})
	}
}
