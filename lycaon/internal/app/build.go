package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/app/boards"
	"github.com/lycaon/lycaon/internal/app/configuration"
	"github.com/lycaon/lycaon/internal/app/decisions"
	"github.com/lycaon/lycaon/internal/app/delegations"
	"github.com/lycaon/lycaon/internal/app/deviceidentity"
	"github.com/lycaon/lycaon/internal/app/eventing"
	"github.com/lycaon/lycaon/internal/app/execution"
	"github.com/lycaon/lycaon/internal/app/interactions"
	"github.com/lycaon/lycaon/internal/app/persistence"
	"github.com/lycaon/lycaon/internal/app/processes"
	"github.com/lycaon/lycaon/internal/app/providers"
	"github.com/lycaon/lycaon/internal/app/scanning"
	"github.com/lycaon/lycaon/internal/app/security"
	"github.com/lycaon/lycaon/internal/app/server"
	"github.com/lycaon/lycaon/internal/app/sessions"
	"github.com/lycaon/lycaon/internal/app/workflows"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/harnessfixture"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/startupprotocol"
	"github.com/lycaon/lycaon/internal/worker"
)

type gitRuntime struct {
	mgr      *git.Manager
	status   *git.StatusCache
	repoSets *git.RepoSetCache
}

type workerCoordination struct {
	cfg            worker.WorkersConfig
	merge          *worker.MergeService
	poller         *worker.LocalWorkerPoller
	harnessWorkers *harnessfixture.Workers
}

type serveBuilder struct {
	execution    execution.Runtime
	identity     deviceidentity.Credentials
	interactions interactions.Runtime
	providers    providers.Runtime
	catalog      configuration.Catalog
	settings     configuration.Runtime
	startup      startupBootstrap
	storage      persistence.Runtime
	decisions    decisions.Runtime
	agents       configuration.Agents
	workflows    *workflows.Runtime
	scanning     *scanning.Runtime
	sessions     *sessions.Runtime
	delegations  *delegations.Runtime
	boards       *boards.Runtime
	security     *security.Runtime
	events       *eventing.Runtime
	processes    *processes.Runtime
	git          gitRuntime
	worker       workerCoordination
	server       server.Runtime
}

// Build wires all serve subsystems and validates boot configuration.
func Build(ctx context.Context, cfg configuration.Config) (*ServeApp, error) {
	buildPerf := observability.StartPerformanceOperation("app.build", nil)
	buildOutcome := "error"
	defer func() { buildPerf.End(buildOutcome) }()
	resources := newRuntimeResources()
	b := &serveBuilder{startup: startupBootstrap{ctx: ctx, cfg: cfg, resources: resources, recovery: bootrecovery.New()},
		storage: persistence.Runtime{}}
	for _, step := range []struct {
		name  string
		phase startupprotocol.Phase
		fn    func() error
	}{
		{"observability", startupprotocol.PhaseObservability, b.startup.initObservability},
		{"store", startupprotocol.PhaseStore, func() error {
			path, err := cfg.ResolveDBPath()
			if err != nil {
				return err
			}
			if err := b.storage.Open(ctx, path, cfg.Startup, b.startup.logger, resources); err != nil {
				return err
			}
			b.security = security.New(ctx, b.storage.Database, b.storage.Sessions, b.storage.Projects, nil)
			resources.Track("secret-redactors", 120, func(context.Context) error { b.security.ReleaseRedactors(); return nil })
			return nil
		}},
		{"config", startupprotocol.PhaseConfiguration, b.loadConfig},
		{"egress-broker", startupprotocol.PhaseConfiguration, func() error {
			b.processes = processes.New(b.startup.logger, resources)
			return b.processes.StartEgress(b.storage.Directory)
		}},
		{"refusal-watch", startupprotocol.PhaseConfiguration, func() error { return b.processes.StartRefusalWatch(b.storage.Directory) }},
		{"user-path", startupprotocol.PhaseUserPath, func() error { return b.processes.ResolvePath(ctx) }},
		{"credential-floors", startupprotocol.PhaseCredentials, func() error { return b.security.Detections.LoadFloors() }},
		{"host_resources", startupprotocol.PhaseHostResources, func() error { return b.settings.BuildHostResources(b.storage.Directory) }},
		{"llm", startupprotocol.PhaseProviders, func() error {
			return b.providers.Build(ctx, providers.Options{Client: cfg.TestLLMClient, Pricer: cfg.TestCostPricer, Startup: cfg.Startup}, b.storage.Database, b.storage.Directory, b.settings.Service, resources, b.startup.recovery)
		}},
		{"tool-runtime", startupprotocol.PhaseTools, b.wireToolRuntime},
		{"presence", startupprotocol.PhaseTools, func() error { return b.security.BuildPresence(b.execution.Host.Executor.Secrets) }},
		{"agents", startupprotocol.PhaseAgents, func() error { return b.agents.Load(ctx) }},
		{"session-manager", startupprotocol.PhaseSessions, b.wireSessionManager},
		{"oar-block-plane", startupprotocol.PhasePolicy, b.wireOARBlockPlane},
		{"events", startupprotocol.PhaseEvents, b.wireEvents},
		{"authz-capturer", startupprotocol.PhasePolicy, b.assertAuthzCapturer},
		{"workflows", startupprotocol.PhaseWorkflows, b.wireWorkflows},
		{"delegation-workers", startupprotocol.PhaseWorkers, b.wireDelegationWorkers},
		{"scan", startupprotocol.PhaseScan, b.wireScan},
		{"board-research", startupprotocol.PhaseResearch, b.wireBoardAndResearch},
		{"grounding-findings", startupprotocol.PhaseGrounding, b.wireGroundingAndFindings},
		{"coordinator-runtime", startupprotocol.PhaseCoordinator, b.wireCoordinatorRuntime},
		{"coordinator-tools", startupprotocol.PhaseCoordinator, b.registerCoordinatorTools},
		{"orchestrator", startupprotocol.PhaseCoordinator, b.wireOrchestrator},
		{"runtime-services", startupprotocol.PhaseServer, b.wireRuntimeServices},
		{"mcp", startupprotocol.PhaseServer, b.wireMCP},
		// The API is built once, after every service it serves exists.
		{"server", startupprotocol.PhaseServices, b.wireServer},
		// The deferred gate stays closed until every producer is wired.
		{"seal-approvals", startupprotocol.PhasePolicy, b.sealApprovalGate},
		// Run recovery after subsystem owners are constructed.
		{"boot-recovery", startupprotocol.PhaseRecovery, b.runBuildRecovery},
	} {
		if err := ctx.Err(); err != nil {
			// Shutdown arrived mid-startup; stop before starting more children.
			b.startup.closeFailedBuild(context.WithoutCancel(ctx))
			return nil, fmt.Errorf("startup interrupted before %s: %w", step.name, err)
		}
		if cfg.Startup != nil {
			if err := cfg.Startup.Phase(step.phase); err != nil {
				b.startup.closeFailedBuild(ctx)
				return nil, fmt.Errorf("startup protocol: %w", err)
			}
		}
		err := step.fn()
		buildPerf.Mark(step.name)
		if err != nil {
			if step.name == "store" && errors.Is(err, db.ErrStoreIncompatible) {
				// Recovery mode keeps restore available for the intact store.
				if cfg.Startup != nil {
					if protocolErr := cfg.Startup.Phase(startupprotocol.PhaseRecovery); protocolErr != nil {
						b.startup.closeFailedBuild(ctx)
						return nil, fmt.Errorf("startup protocol: %w", protocolErr)
					}
				}
				app, recoveryErr := buildRecoveryApp(ctx, cfg, b, err)
				if recoveryErr != nil {
					b.startup.closeFailedBuild(ctx)
				} else {
					buildOutcome = "recovery"
				}
				return app, recoveryErr
			}
			b.startup.closeFailedBuild(ctx)
			return nil, fmt.Errorf("%s: %w", step.name, err)
		}
	}
	buildOutcome = "ok"
	return b.serveApp(), nil
}
