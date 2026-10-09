package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/app/configuration"
	"github.com/lycaon/lycaon/internal/app/decisions"
	"github.com/lycaon/lycaon/internal/app/eventing"
	"github.com/lycaon/lycaon/internal/app/persistence"
	"github.com/lycaon/lycaon/internal/app/processes"
	"github.com/lycaon/lycaon/internal/app/providers"
	"github.com/lycaon/lycaon/internal/app/security"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/browser/preview"
	"github.com/lycaon/lycaon/internal/call"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/harnessfixture"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/historyretention"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostidentity"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/projectliveness"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanexecution "github.com/lycaon/lycaon/internal/scan/execution"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/startupprotocol"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/usernotice"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workspace"
)

type serveBuilder struct {
	providers providers.Runtime
	catalog   configuration.Catalog
	settings  configuration.Runtime
	startup   startupBootstrap
	storage   persistence.Runtime
	decisions decisions.Runtime
	agents    configuration.Agents

	apiToken            string
	tokenGenerated      bool
	hostIdentity        hostidentity.Identity
	workerBranchRoot    string
	workerSeedRoot      string
	bundledRules        map[string]*rules.RulesConfig
	workerToolBudgetFor func(string) spawn.WorkerToolBudget

	sourceScopes  *sourcescope.Provider
	bgRegistry    *bgprocess.Registry
	heldCalls     *heldcall.Registry
	browserPool   *browser.Pool
	browserRaster *browser.Rasterizer
	pageRegistry  *pagesession.Registry
	previewCtrl   *preview.Controller

	webWarmer   *webresearch.Warmer
	warmRunner  *webresearch.WarmRunner
	invocations invocation.Recorder
	toolRuntime *toolhost.Runtime
	// turnLoads is the loaded-schema ledger shared by request_tools and the coordinator turn.
	turnLoads *turnload.Ledger
	// decider is the local decision model; nil resolves to an absent engine.
	// rerank carries it with the catalog policies into every ranking site.
	toolReg          *tools.ExecutorRegistry
	security         *security.Runtime
	events           *eventing.Runtime
	processes        *processes.Runtime
	rejectFmt        *guidance.StaticRejectFormatter
	hintCfg          *guidance.HintConfig
	promptEngine     *prompts.FileTemplateEngine
	mgr              *session.Manager
	projectLiveness  *projectliveness.Tracker
	checkpointMgr    hitl.CheckpointManager
	workersCfg       worker.WorkersConfig
	delegationStore  *delegation.SQLStore
	blueprintMgr     *blueprint.Manager
	manifestRegistry *workflowdef.Registry
	// manifestResolver is the one workflow catalog seam: discovery and start
	// read the same source.
	manifestResolver     workflow.ManifestResolver
	sessionWorkflowStore *workflow.SessionWorkflowSQLStore
	workflowStore        *workflow.SQLStore
	workflowMgr          *workflow.RunManager
	evidenceStore        inspector.EvidenceStore
	simpleInspector      *inspector.SimpleInspector
	gitMgr               *git.Manager
	gitStatusCache       *git.StatusCache
	gitRepoSetCache      *git.RepoSetCache
	gatesCfg             scancfg.GatesConfig
	scanStore            *scan.SQLStore
	scanCoordinator      *scan.CoordinatorImpl
	scanCadence          *scancadence.Service
	securityCloseout     *scan.SecurityCloseoutChecker
	workerQueue          *worker.SQLQueue
	condReg              *conditions.ConditionRegistry
	workflowComposer     *workflow.Composer
	workflowPersister    *workflow.Persister
	ruleEngine           *rules.PostureRuleEngine
	projectRulesOverlay  *rules.ProjectRulesOverlay
	criteriaChecker      delegation.CriteriaChecker
	injectRenderer       *prompts.InjectRenderer
	workerExec           *worker.LocalWorkerExecutor
	wsMgr                *workspace.Manager
	workerCancelSvc      *worker.CancelService
	workerMergeSvc       *worker.MergeService
	answerDecisionSvc    *worker.AnswerDecisionService
	workerBudgetLedger   *worker.SQLBudgetLedger
	delegationMgr        *delegation.Manager
	playbookMatcher      *prompts.PlaybookMatcher
	scannerReg           scan.CodeScannerRegistry
	scanRunner           *scanexecution.Runner
	scanTriggers         *scan.TriggerService
	scanObligation       *scan.WorkflowObligation
	scanGuidance         *scan.SessionGuidanceAdapter
	repoProvider         repoinfo.Provider
	boardSnap            *board.SnapshotBuilder
	webResearchCreds     *webresearch.CredentialStore
	webResearchRuntime   webresearch.Runtime
	coordRuntime         *coordinator.Runtime
	groundingSvc         *toolhost.GroundingService
	findingsStore        *findings.SQLStore
	progressStore        *progress.SQLStore
	visualStore          visual.Store
	historyStorage       *historyretention.Service
	decisionStore        session.DecisionStore
	callMgr              *call.SQLManager
	parentWorkerWaiter   *worker.ParentWorkerWaiter
	userNoticeCatalog    *usernotice.Catalog
	srv                  *api.Server
	mcpReg               *mcp.Runtime
	orch                 orchestration.Orchestrator
	workerPoller         *worker.LocalWorkerPoller
	authzCapturer        *authzcontext.Capturer
	socketCapabilityRT   *approvalstate.SocketCapabilityRuntime
	gateRepeatRT         *approvalstate.GateRepeatLedger
	webDiscoverer        webresearch.DirectDiscovererFactory
	harnessWorkers       *harnessfixture.Workers
	directIPCapabilityRT *approvalstate.DirectIPCapabilityRuntime
	sandboxWriteRootRT   *approvalstate.SandboxPathGrantRuntime
	sandboxReadPathRT    *approvalstate.SandboxPathGrantRuntime
	sandboxListenRT      *approvalstate.SandboxPortGrantRuntime
	sandboxLoopbackRT    *approvalstate.SandboxPortGrantRuntime
	grantedPathRT        *grantedpath.Runtime
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
		{"presence", startupprotocol.PhaseTools, func() error { return b.security.BuildPresence(b.toolRuntime.Executor.Secrets) }},
		{"agents", startupprotocol.PhaseAgents, func() error { return b.agents.Load(ctx) }},
		{"session-manager", startupprotocol.PhaseSessions, sessionWiring{b}.wireSessionManager},
		{"oar-block-plane", startupprotocol.PhasePolicy, toolWiring{b}.wireOARBlockPlane},
		{"events", startupprotocol.PhaseEvents, b.wireEvents},
		{"authz-capturer", startupprotocol.PhasePolicy, sessionWiring{b}.assertAuthzCapturer},
		{"workflows", startupprotocol.PhaseWorkflows, boardWiring{b}.wireWorkflows},
		{"delegation-workers", startupprotocol.PhaseWorkers, delegationWiring{b}.wireDelegationWorkers},
		{"scan", startupprotocol.PhaseScan, toolWiring{b}.wireScan},
		{"board-research", startupprotocol.PhaseResearch, boardWiring{b}.wireBoardAndResearch},
		{"grounding-findings", startupprotocol.PhaseGrounding, boardWiring{b}.wireGroundingAndFindings},
		{"coordinator-runtime", startupprotocol.PhaseCoordinator, toolWiring{b}.wireCoordinatorRuntime},
		{"coordinator-tools", startupprotocol.PhaseCoordinator, toolWiring{b}.registerCoordinatorTools},
		{"orchestrator", startupprotocol.PhaseCoordinator, serverWiring{b}.wireOrchestrator},
		{"runtime-services", startupprotocol.PhaseServer, serverWiring{b}.wireRuntimeServices},
		{"mcp", startupprotocol.PhaseServer, toolWiring{b}.wireMCP},
		// The API is built once, after every service it serves exists.
		{"server", startupprotocol.PhaseServices, serverWiring{b}.wireServer},
		// The deferred gate stays closed until every producer is wired.
		{"seal-approvals", startupprotocol.PhasePolicy, serverWiring{b}.sealApprovalGate},
		// Run recovery after subsystem owners are constructed.
		{"boot-recovery", startupprotocol.PhaseRecovery, delegationWiring{b}.runBuildRecovery},
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
		b.startup.resources.capture(b)
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
	return serverWiring{b}.serveApp(), nil
}
