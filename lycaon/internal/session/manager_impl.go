package session

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/call"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/governance"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectliveness"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/resourcelifecycle"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretmint"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	sessioncatalog "github.com/lycaon/lycaon/internal/session/catalog"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	"github.com/lycaon/lycaon/internal/session/stream"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/sourcerewind"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/internal/workscope"
	"github.com/lycaon/lycaon/pkg/api"
	"golang.org/x/sync/singleflight"
)

type Manager struct {
	catalog                    sessioncatalog.Service
	executionCheckpoints       ExecutionCheckpointSource
	store                      Store
	credentialFiles            tools.CredentialFiles
	secretFP                   *secretmatch.Fingerprinter
	harvestHas                 HarvestedFingerprint
	ignoredCredentialCandidate func(context.Context, string, string) bool
	credentialSlots            func(context.Context, *api.Session) *secretmint.Inspector
	indexWarmer                IndexWarmer
	llm                        modelcall.LLMClient
	llmSvc                     *llm.Service
	compactor                  compaction.ContextCompactor
	compactionRunner           *CompactionRunner
	cost                       cost.CostTracker
	tools                      tools.ToolRegistry
	invocations                invocation.Recorder
	toolInvoker                tools.ToolInvoker
	toolLister                 tools.ToolProfileLister
	sourceLedger               sourceledger.Recorder
	sourceHistory              tools.SourceHistory
	sourceCommands             sourceledger.CommandWindowOpener
	sourceGitMutations         tools.SourceGitMutations
	sourceObservations         *sourceledger.Inventory
	sourceCheckpoints          sourceReviewCheckpointer
	sourceMutations            sourceeffect.Journal
	sourceRewinds              *sourcerewind.Service
	editorDocuments            tools.EditorDocuments
	agentPresence              *agentpresence.Tracker
	verificationSource         func(context.Context, string) (string, string)
	cfg                        settings.SessionLimits
	limits                     LimitsProvider
	agents                     AgentProfileResolver
	prompts                    prompts.PromptTemplateEngine
	postures                   *PostureRegistry
	postureOverlay             scopedstore.LRU[*PostureRegistry]
	doomLoop                   loopguard.DoomLoopGuard
	grounding                  GroundingHook
	rejectFmt                  *guidance.StaticRejectFormatter
	// oarPipeline and oarRenderer enforce structured output bindings.
	oarPipeline             *oar.GuardPipeline
	oarRenderer             *oar.Renderer
	mcpRuntime              MCPRuntimeView
	workflows               WorkflowSessionView
	reportDocuments         ReportDocumentChecker
	scanEvidenceRuns        ScanEvidenceRuns
	workflowToolAccess      WorkflowToolAccessView
	rules                   RuleEvaluator
	streamsOnce             sync.Once
	streams                 *stream.State
	promptState             promptstate.State
	engineStopping          atomic.Bool
	engineWork              workscope.Group
	stopState               lifecycle.State
	sessionWorkerAbort      sessionWorkerAbort
	sessionWorkflowStop     sessionWorkflowStop
	sessionCheckpointStop   sessionCheckpointStop
	bgRegistry              *bgprocess.Registry
	heldCalls               *heldcall.Registry
	processReports          processReports
	pageRegistry            *pagesession.Registry
	resources               *resourcelifecycle.Registry
	events                  *events.Publisher
	turnFailure             TurnFailureSink
	scanGuidance            ScanGuidanceHook
	coordinatorFrame        inject.CoordinatorTurnFrameSource
	workerContext           assembly.WorkerContextBuilder
	redactMessageForStorage func(ctx context.Context, msg api.Message) (api.Message, bool)
	secretSweeps            sweepQueue
	mintedCredentials       func() MintedCredentialSource
	rememberSecrets         secretmatch.RememberFunc
	workflowHints           *guidance.HintConfig
	gateFeedback            *feedback.GateFeedbackCatalog
	workspaceCheck          workercompletion.WorkspaceChangeChecker
	evidenceStore           inspector.EvidenceStore
	verifyConfig            VerifyConfigResolver
	toolRejectFormatter     *guidance.ToolRejectFormatter
	toolOutputEnricher      *guidance.ToolOutputEnricher
	boardBuilder            assembly.BoardSnapshotBuilder
	boardFormatter          assembly.BoardPackFormatter
	includeScanLegend       func() bool
	loopWorkflowSource      loopwake.LoopWorkflowSource
	coordinatorRuntime      *coordinator.Runtime
	coordinatorRuntimeOnce  sync.Once
	delegations             DelegationLegLookup
	profileRuntimeRules     *toolpolicy.ProfileRuntimeRules
	planToolStash           *PlanToolStash
	workerQueue             WorkerCycleLister
	findings                findings.Store
	peerRejections          *PeerRejectionFeed
	progress                progress.RunScopedStore
	visual                  visual.Store
	queue                   *queue.Store
	decisions               DecisionStore
	writeRootRuntime        *approvalstate.SandboxPathGrantRuntime
	listenRuntime           *approvalstate.SandboxPortGrantRuntime
	loopbackRuntime         *approvalstate.SandboxPortGrantRuntime
	toolApprovalCoalesce    *approvalstate.ToolApprovalCoalesce
	gateRepeatLedger        *approvalstate.GateRepeatLedger
	// turnReleaseTimeout bounds how long a stop waits for a cancelled turn to
	// release its session before recording the stop without it.
	turnReleaseTimeout time.Duration
	// progressClosureExpect tracks unfinished progress after worker completion.
	progressClosureExpect       scopedstore.LRU[guard.ProgressClosureBaseline]
	closeout                    closeoutLifecycle
	deferredTurnSettlement      deferredTurnSettlementStore
	deferredWorkflowCompletions sync.Map // session id -> completed run id
	begunSubmissions            begunSubmissions
	// coordinatorBatchTurn latches synthesis within a turn.
	coordinatorBatchTurn scopedstore.LRU[bool]
	scanWaits            ScanWaitState
	overlayPromoter      OverlayPromoter
	callManager          call.CallManager
	workerTouches        *WorkerTouchLedger
	checkpointCapture    sessioncheckpoint.Capture
	scratch              *scratch.Folders
	// workerDigests holds pending worker wake payloads.
	workerDigests    scopedstore.LRU[string]
	synthesisCurator llm.Curator
	// mergeReconcile holds paths open during promotion.
	mergeReconcile             scopedstore.LRU[map[string]struct{}]
	compactionTokenCalibration scopedstore.LRU[compaction.PromptTokenCalibration]
	// promotePathStatus caches promotion details for board injects.
	promotePathStatus       scopedstore.LRU[*promotePathSessionStore]
	workerGracefulCancelMu  sync.Mutex
	workerGracefulCancel    map[string]*gracefulCancelRegistration
	webResearchConfig       *webresearch.ConfigStore
	turnLoads               *turnload.Ledger
	decider                 decide.Decider
	skillBody               SkillBodyRenderer
	projectSandboxReconcile ProjectSandboxReconcile
	projects                project.Registry
	repoProvider            repoinfo.Provider
	dataDir                 string
	mutationGate            *project.MutationGate
	projectLiveness         *projectliveness.Tracker
	promotionHook           promotionHook
	agentsMDCache           scopedstore.LRU[*governance.AgentsMDSessionState]
	// agentsMDWarm coalesces concurrent policy index builds.
	agentsMDWarm singleflight.Group
	// agentsMDListIndex overrides governance.ListIndex in tests; nil uses it.
	agentsMDListIndex       func(absRoot string) ([]governance.ResolvedAgentsMD, error)
	trustSurfaces           *settings.TrustSurfacesStore
	skillsGate              *settings.ProjectSurfaceGate
	skillsCache             skills.ProjectCache
	hostResources           *hostresources.Service
	authzSealer             *authzcontext.Sealer
	authzSealRequired       bool
	directIPReconstructHook DirectIPReconstructHook
	reconstructedDirectIPMu sync.Mutex
	reconstructedDirectIP   map[string]map[string]struct{}
	curation                promptCurations
	roundEndDrains          roundEndDrains
	loopbackProv            LoopbackProvenanceResolver
}

// SetLoopbackProvenance installs the provenance resolver for task container recording.
func (m *Manager) SetLoopbackProvenance(p LoopbackProvenanceResolver) {
	if m != nil {
		m.loopbackProv = p
	}
}

// LimitsProvider supplies effective session limits for a project.
type LimitsProvider interface {
	SessionLimits(projectDir string) settings.SessionLimits
}

func NewManager(store Store, client modelcall.LLMClient, registry tools.ToolRegistry, cfg settings.SessionLimits) *Manager {
	return NewManagerWithLLMService(store, client, nil, registry, cfg, nil)
}

// NewManagerWithLLMService optionally wires provider settings, cost tracking, and model routing.
func NewManagerWithLLMService(store Store, client modelcall.LLMClient, svc *llm.Service, registry tools.ToolRegistry, cfg settings.SessionLimits, tracker cost.CostTracker) *Manager {
	cfg = settings.NormalizeSessionLimits(cfg)
	m := &Manager{
		store:              store,
		catalog:            sessioncatalog.New(store),
		llm:                client,
		llmSvc:             svc,
		cost:               tracker,
		tools:              registry,
		cfg:                cfg,
		compactionRunner:   NewCompactionRunner(),
		planToolStash:      NewPlanToolStash(),
		queue:              queue.New(),
		turnReleaseTimeout: DefaultTurnReleaseTimeout,
	}
	m.ensureResourceRegistry()
	return m
}

func (m *Manager) SessionByID(ctx context.Context, id string) (*api.Session, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("session manager not configured")
	}
	return m.store.Get(ctx, id)
}

func (m *Manager) CostTracker() cost.CostTracker {
	return m.cost
}

func (m *Manager) effectiveLimits(ctx context.Context, sess *api.Session) settings.SessionLimits {
	projectDir := m.overlayProjectDir(ctx, sess)
	var lim settings.SessionLimits
	if m.limits != nil && sess != nil {
		lim = m.limits.SessionLimits(projectDir)
	} else {
		lim = m.cfg
	}
	_, derived := m.liveBudgetResolveForRoots(m.overlayRootPaths(ctx, sess))
	lim = settings.ApplyDerivedSessionLimits(lim, derived)
	return ApplyWorkerMaxToolLoops(lim, sess)
}
