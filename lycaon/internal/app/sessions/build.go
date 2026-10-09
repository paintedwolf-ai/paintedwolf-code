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
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
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
	deps.Security.BindRemember(mgr.ToolPolicy.SetRememberSecrets)
	deps.Execution.Host.Skills.BindTurnSources(mgr.Coordinator.Loading.ResolveToolRequest, mgr.Coordinator.Loading.RecordToolRequest, mgr.Coordinator.Loading.LookupSkills)
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

	mgr.SetSourceLedger(deps.Storage.SourceLedger)
	mgr.Profiles.SetAgentRegistry(deps.Agents.Registry)
	mgr.Profiles.SetHostResources(deps.Settings.HostResources)
	if deps.Settings.HostResources != nil && deps.Settings.Service != nil && deps.Settings.Service.Approvals != nil {
		deps.Settings.HostResources.SetPolicyBinder(configuration.HostResourcePolicyBinder(deps.Settings.Service.Approvals, mgr.Profiles))
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
	if deps.Settings.Service != nil {
		if deps.TestSessionLimits == nil {
			mgr.Limits.SetProvider(settings.ProjectLimitsAdapter{Store: deps.Settings.Service.Limits})
		}
		mgr.SetEffectiveCatalogDeps(deps.Catalog.ModuleRoot, deps.Catalog.Effective, deps.Settings.Service.TrustSurfaces)
		mgr.Profiles.SetSkillsGate(deps.Settings.ProjectSurfaceGate(projectcontrib.SurfaceSkills, deps.Storage.Projects))
	}
	if deps.Catalog.ViewCache != nil {
		mgr.Catalog.SetCatalogViewCache(deps.Catalog.ViewCache)
	}

	workerToolBudgetFor := func(projectDir string) spawn.WorkerToolBudget {
		if deps.Settings.Service == nil {
			return deps.Settings.SessionLimits.WorkerToolBudget()
		}
		if !deps.Settings.ProjectSurfaceGate(projectcontrib.SurfaceProjectSettings, deps.Storage.Projects).AppliesPath(context.Background(), projectDir) {
			projectDir = ""
		}
		return settings.ProjectLimitsAdapter{Store: deps.Settings.Service.Limits}.SessionLimits(projectDir).WorkerToolBudget()
	}

	wireSessionToolSources(mgr, deps.Execution.Host, deps.Settings.HostResources, workerToolBudgetFor)

	if err := deps.Security.BuildAuthorization(deps.Catalog.ModuleRoot, deps.Agents.ToolProfiles, deps.Settings.Service.Approvals, deps.Execution.Host.Authority.ApprovalGate, deps.Execution.Host.Registry.List, mgr.Profiles.ResolveToolAccess, workerToolBudgetFor); err != nil {
		return nil, err
	}
	mgr.Runner.Authorization.SetSealer(deps.Security.Authority.Sealer)
	if compactor, err := loadCompactor(deps.Providers.Service, deps.Catalog.ModuleRoot, deps.Providers.Costs); err == nil && compactor != nil {
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

func wireSessionToolSources(mgr *session.Host, host *toolhost.Runtime, hostResources *hostresources.Service, workerToolBudgetFor func(string) spawn.WorkerToolBudget) {
	if host != nil && host.Boundary != nil {
		host.Boundary.SetProfileSource(func(ctx context.Context, sessionID string) []sandbox.ToolProfile {
			view := mgr.Catalog.ViewForSessionID(ctx, sessionID)
			if view == nil {
				return nil
			}
			return view.ToolProfiles
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
			return loaded
		})
		host.Skills.SetSkillTemplateVars(func(_ context.Context, tctx tools.ToolContext) map[string]any {
			budget := spawn.DefaultWorkerToolBudget()
			if workerToolBudgetFor != nil {
				budget = workerToolBudgetFor(strings.TrimSpace(tctx.ActiveRootPath()))
			}
			return spawn.PolicyTemplateVars(budget)
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
