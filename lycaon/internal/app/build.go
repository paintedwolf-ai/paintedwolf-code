package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/lycaon/lycaon/internal/agentpresence"
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
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/harnessfixture"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/historyretention"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostidentity"
	"github.com/lycaon/lycaon/internal/hostlock"
	"github.com/lycaon/lycaon/internal/hostpower"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectignore"
	"github.com/lycaon/lycaon/internal/projectliveness"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanexecution "github.com/lycaon/lycaon/internal/scan/execution"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/startupprotocol"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/usernotice"
	"github.com/lycaon/lycaon/internal/userpath"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/webindex"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workspace"
)

type serveBuilder struct {
	ctx       context.Context
	cfg       Config
	logger    *slog.Logger
	resources *runtimeResources
	recovery  *bootrecovery.Registry

	addr                string
	configRoot          string
	effective           *extpacks.EffectiveCatalog
	viewCache           *catalogview.Cache
	deviceView          *catalogview.View
	sessionCfg          settings.SessionLimits
	storeRevision       uint64
	retentionCfg        db.RetentionConfig
	apiToken            string
	tokenGenerated      bool
	hostIdentity        hostidentity.Identity
	workerBranchRoot    string
	workerSeedRoot      string
	bundledRules        map[string]*rules.RulesConfig
	workerToolBudgetFor func(string) spawn.WorkerToolBudget

	db        *db.Store
	storePath string
	dataDir   string
	// egressBrokerBound records that this build owns the mediation front door.
	egressBrokerBound bool
	// refusalWatchStarted records that this build reads kernel refusal reports.
	refusalWatchStarted bool
	// storeClaim's release transfers to runtimeResources after construction.
	storeClaim        *hostlock.Claim
	store             *store.SQL
	registry          *project.SQLRegistry
	sourceLedger      *sourceledger.Store
	sourceScopes      *sourcescope.Provider
	sourceFeedUnbinds []func()
	hub               events.ReplayHub
	presence          *events.Presence
	eventPub          *events.Publisher
	agentPresence     *agentpresence.Tracker
	eventOutbox       *eventoutbox.Outbox
	bgRegistry        *bgprocess.Registry
	heldCalls         *heldcall.Registry
	browserPool       *browser.Pool
	browserRaster     *browser.Rasterizer
	pageRegistry      *pagesession.Registry
	previewCtrl       *preview.Controller

	mockLLM              modelcall.LLMClient
	manualLLM            *llm.ManualProvider
	llmSvc               *llm.Service
	synthesisCurator     llm.Curator
	webIndex             *webindex.Store
	upgradeRecoveryReady func() error
	webWarmer            *webresearch.Warmer
	warmRunner           *webresearch.WarmRunner
	costTracker          cost.CostTracker
	invocations          invocation.Recorder
	pricingHost          *settings.PricingHost
	settingsSvc          *settings.Service
	hostResources        *hostresources.Service
	hostPower            *hostpower.Controller
	userPath             userpath.Snapshot
	toolRuntime          *toolhost.Runtime
	// turnLoads is the loaded-schema ledger shared by request_tools and the coordinator turn.
	turnLoads *turnload.Ledger
	// decider is the local decision model; nil resolves to an absent engine.
	// rerank carries it with the catalog policies into every ranking site.
	decider             decide.Decider
	rerank              decide.Reranker
	toolReg             *tools.ExecutorRegistry
	detections          detectionRuntime
	secretMatcher       *secretmatch.Matcher
	secretIgnores       *projectignore.SecretService
	secretHarvest       *secretharvest.Runtime
	secretCaps          *secretcap.Service
	secretFingerprinter *secretmatch.Fingerprinter
	// presenceBroker verifies that a person is at this device; vaultUnlocks
	// holds the chats their presence unlocked.
	presenceBroker   *presence.Broker
	vaultUnlocks     *presence.Unlocks
	rejectFmt        *guidance.StaticRejectFormatter
	hintCfg          *guidance.HintConfig
	agentRegistry    *orchestration.MemoryAgentRegistry
	toolProfiles     []sandbox.ToolProfile
	postureRegistry  *session.PostureRegistry
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
	mcpReg               *mcp.RegistryImpl
	orch                 orchestration.Orchestrator
	workerPoller         *worker.LocalWorkerPoller
	authzCapturer        *authzcontext.Capturer
	socketCapabilityRT   *approvalstate.SocketCapabilityRuntime
	gateRepeatRT         *approvalstate.GateRepeatLedger
	secretSpans          *secretspan.Screener
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
func Build(ctx context.Context, cfg Config) (*ServeApp, error) {
	buildPerf := observability.StartPerformanceOperation("app.build", nil)
	buildOutcome := "error"
	defer func() { buildPerf.End(buildOutcome) }()
	b := &serveBuilder{ctx: ctx, cfg: cfg, resources: newRuntimeResources(), recovery: bootrecovery.New()}
	for _, step := range []struct {
		name  string
		phase startupprotocol.Phase
		fn    func() error
	}{
		{"observability", startupprotocol.PhaseObservability, b.initObservability},
		{"store", startupprotocol.PhaseStore, b.openStore},
		{"config", startupprotocol.PhaseConfiguration, b.loadConfig},
		{"egress-broker", startupprotocol.PhaseConfiguration, b.wireEgressBroker},
		{"refusal-watch", startupprotocol.PhaseConfiguration, b.wireRefusalWatch},
		{"user-path", startupprotocol.PhaseUserPath, b.wireUserPath},
		{"credential-floors", startupprotocol.PhaseCredentials, sessionWiring{b}.wireCredentialFloors},
		{"host_resources", startupprotocol.PhaseHostResources, b.wireHostResources},
		{"llm", startupprotocol.PhaseProviders, b.wireLLM},
		{"tool-runtime", startupprotocol.PhaseTools, b.wireToolRuntime},
		{"presence", startupprotocol.PhaseTools, b.wirePresence},
		{"agents", startupprotocol.PhaseAgents, b.wireAgents},
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
			b.closeFailedBuild(context.WithoutCancel(ctx))
			return nil, fmt.Errorf("startup interrupted before %s: %w", step.name, err)
		}
		if cfg.Startup != nil {
			if err := cfg.Startup.Phase(step.phase); err != nil {
				b.closeFailedBuild(ctx)
				return nil, fmt.Errorf("startup protocol: %w", err)
			}
		}
		err := step.fn()
		buildPerf.Mark(step.name)
		b.resources.capture(b)
		if err != nil {
			if step.name == "store" && errors.Is(err, db.ErrStoreIncompatible) {
				// Recovery mode keeps restore available for the intact store.
				if cfg.Startup != nil {
					if protocolErr := cfg.Startup.Phase(startupprotocol.PhaseRecovery); protocolErr != nil {
						b.closeFailedBuild(ctx)
						return nil, fmt.Errorf("startup protocol: %w", protocolErr)
					}
				}
				app, recoveryErr := buildRecoveryApp(ctx, cfg, b, err)
				if recoveryErr != nil {
					b.closeFailedBuild(ctx)
				} else {
					buildOutcome = "recovery"
				}
				return app, recoveryErr
			}
			b.closeFailedBuild(ctx)
			return nil, fmt.Errorf("%s: %w", step.name, err)
		}
	}
	buildOutcome = "ok"
	return serverWiring{b}.serveApp(), nil
}

func (b *serveBuilder) closeFailedBuild(ctx context.Context) {
	b.resources.capture(b)
	_ = b.resources.Close(ctx)
}
