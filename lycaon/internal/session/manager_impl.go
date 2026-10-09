package session

import (
	"context"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/resourcelifecycle"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/session/authorization"
	"github.com/lycaon/lycaon/internal/session/batchcontrol"
	sessioncatalog "github.com/lycaon/lycaon/internal/session/catalog"
	"github.com/lycaon/lycaon/internal/session/chats"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	"github.com/lycaon/lycaon/internal/session/closeoutassembly"
	"github.com/lycaon/lycaon/internal/session/closeoutevidence"
	"github.com/lycaon/lycaon/internal/session/closeouts"
	"github.com/lycaon/lycaon/internal/session/curation"
	"github.com/lycaon/lycaon/internal/session/draftqueue"
	"github.com/lycaon/lycaon/internal/session/execution"
	"github.com/lycaon/lycaon/internal/session/guidancedelivery"
	"github.com/lycaon/lycaon/internal/session/history"
	"github.com/lycaon/lycaon/internal/session/instructions"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	sessionlimits "github.com/lycaon/lycaon/internal/session/limits"
	"github.com/lycaon/lycaon/internal/session/loading"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/session/naming"
	sessionobservation "github.com/lycaon/lycaon/internal/session/observation"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/session/policyfeedback"
	"github.com/lycaon/lycaon/internal/session/policyindex"
	"github.com/lycaon/lycaon/internal/session/postturn"
	"github.com/lycaon/lycaon/internal/session/preparation"
	"github.com/lycaon/lycaon/internal/session/processcontrol"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/progressclosure"
	"github.com/lycaon/lycaon/internal/session/projectcontrol"
	"github.com/lycaon/lycaon/internal/session/promotionstate"
	"github.com/lycaon/lycaon/internal/session/protection"
	"github.com/lycaon/lycaon/internal/session/recovery"
	"github.com/lycaon/lycaon/internal/session/researchwarm"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/sourcebrief"
	"github.com/lycaon/lycaon/internal/session/spendguard"
	"github.com/lycaon/lycaon/internal/session/stopping"
	"github.com/lycaon/lycaon/internal/session/submissions"
	"github.com/lycaon/lycaon/internal/session/submissionstate"
	"github.com/lycaon/lycaon/internal/session/toolcontext"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/session/turnadmission"
	"github.com/lycaon/lycaon/internal/session/turnclock"
	"github.com/lycaon/lycaon/internal/session/turnexecution"
	"github.com/lycaon/lycaon/internal/session/turnguards"
	"github.com/lycaon/lycaon/internal/session/turnnudges"
	"github.com/lycaon/lycaon/internal/session/turnsettlement"
	sessionverification "github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workerexecution"
	"github.com/lycaon/lycaon/internal/session/workerharness"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/session/workerresults"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/pkg/api"
)

type Manager struct {
	Closeout        *closeoutassembly.Service
	ProjectControl  *projectcontrol.Service
	Admission       *turnadmission.Service
	Processes       *processcontrol.Service
	Promotion       *promotionstate.Service
	Guards          *turnguards.Service
	Runner          *turnexecution.Service
	Batch           *batchcontrol.Service
	Nudges          *turnnudges.Service
	Stops           *stopping.Service
	ProgressClosure *progressclosure.Service
	Guidance        *guidancedelivery.Service
	ToolPolicy      *policyfacts.Service
	Feedback        *policyfeedback.Service
	Interruptions   *execution.Recovery
	ToolContext     *toolcontext.Service
	Verification    *sessionverification.Service
	Protection      *protection.Service
	Profiles        *profiles.Service
	Workers         *workerexecution.Service
	Loading         *loading.Service
	Chats           *chats.Service
	Limits          *sessionlimits.Service
	SourceBriefs    *sourcebrief.Service
	Workspace       *sessionscope.Service
	Catalog         sessioncatalog.Service
	Observations    *sessionobservation.Service
	store           Store
	credentialFiles tools.CredentialFiles
	llm             modelcall.LLMClient
	llmSvc          *llm.Service
	cost            cost.CostTracker
	tools           tools.ToolRegistry
	invocations     invocation.Recorder
	sourceLedger    sourceledger.Recorder
	sourceMutations sourceeffect.Journal
	editorDocuments tools.EditorDocuments
	agentPresence   *agentpresence.Tracker

	prompts prompts.PromptTemplateEngine

	doomLoop         loopguard.DoomLoopGuard
	grounding        GroundingHook
	rejectFmt        *guidance.StaticRejectFormatter
	workflows        WorkflowSessionView
	reportDocuments  ReportDocumentChecker
	scanEvidenceRuns ScanEvidenceRuns

	Gate *lifecycle.State

	pageRegistry *pagesession.Registry
	resources    *resourcelifecycle.Registry
	events       *events.Publisher

	scanGuidance     ScanGuidanceHook
	coordinatorFrame inject.CoordinatorTurnFrameSource
	workerContext    assembly.WorkerContextBuilder
	workflowHints    *guidance.HintConfig
	gateFeedback     *feedback.GateFeedbackCatalog
	workspaceCheck   workercompletion.WorkspaceChangeChecker

	toolRejectFormatter    *guidance.ToolRejectFormatter
	toolOutputEnricher     *guidance.ToolOutputEnricher
	boardBuilder           assembly.BoardSnapshotBuilder
	boardFormatter         assembly.BoardPackFormatter
	includeScanLegend      func() bool
	loopWorkflowSource     loopwake.LoopWorkflowSource
	coordinatorRuntime     *coordinator.Runtime
	coordinatorRuntimeOnce sync.Once
	delegations            DelegationLegLookup
	planToolStash          *PlanToolStash
	workerQueue            WorkerCycleLister

	progress             progress.RunScopedStore
	visual               visual.Store
	queue                *queue.Store
	decisions            DecisionStore
	writeRootRuntime     *approvalstate.SandboxPathGrantRuntime
	listenRuntime        *approvalstate.SandboxPortGrantRuntime
	loopbackRuntime      *approvalstate.SandboxPortGrantRuntime
	toolApprovalCoalesce *approvalstate.ToolApprovalCoalesce
	gateRepeatLedger     *approvalstate.GateRepeatLedger
	// turnReleaseTimeout bounds how long a stop waits for a cancelled turn to
	// release its session before recording the stop without it.

	scanWaits         ScanWaitState
	webResearchConfig *webresearch.ConfigStore
	projects          project.Registry
	repoProvider      repoinfo.Provider
	dataDir           string
	PolicyIndex       *policyindex.Service
	Naming            *naming.Service
	Research          *researchwarm.Service
	Captures          *checkpointcontrol.Capture
	Rewinds           *checkpointcontrol.Rewinds
	Transcript        *transcript.Service
	Submissions       *submissions.Service
	Drafts            *draftqueue.Service
	Recovery          *recovery.Service
	trustSurfaces     *settings.TrustSurfacesStore

	loopbackProv LoopbackProvenanceResolver
}

// SetLoopbackProvenance installs the provenance resolver for task container recording.
func (m *Manager) SetLoopbackProvenance(p LoopbackProvenanceResolver) {
	if m != nil {
		m.loopbackProv = p
		m.ToolContext.SetContainers(p)
		m.Chats.SetLoopback(p)
	}
}

func NewManager(store Store, client modelcall.LLMClient, registry tools.ToolRegistry, cfg settings.SessionLimits) *Manager {
	return NewManagerWithLLMService(store, client, nil, registry, cfg, nil)
}

// NewManagerWithLLMService optionally wires provider settings, cost tracking, and model routing.
func NewManagerWithLLMService(store Store, client modelcall.LLMClient, svc *llm.Service, registry tools.ToolRegistry, cfg settings.SessionLimits, tracker cost.CostTracker) *Manager {
	turnAuthorization := authorization.New()
	turnClocks := turnclock.New(store)
	m := &Manager{
		store:         store,
		Protection:    protection.New(),
		Promotion:     promotionstate.New(),
		Gate:          lifecycle.New(store),
		Workspace:     sessionscope.New(store),
		Catalog:       sessioncatalog.New(store),
		llm:           client,
		llmSvc:        svc,
		cost:          tracker,
		tools:         registry,
		planToolStash: NewPlanToolStash(),
		queue:         queue.New(),
	}
	var namers naming.Namers
	if svc != nil && svc.Utility != nil {
		namers = svc
	}
	m.ProgressClosure = progressclosure.New()
	turnCloseouts := closeouts.New(store)
	turnExecution := execution.NewLifetime(m.Gate)
	turnStatus := execution.NewStatus(store)
	turnTurns := execution.NewJournal(store)
	m.Naming = naming.New(store, m.Workspace, namers, tracker)
	m.Recovery = recovery.New(store)
	turnSubmissionState := submissionstate.New(store, m.queue)
	m.Drafts = draftqueue.New(store, m.queue, turnSubmissionState.Publish)
	m.Transcript = transcript.New(store, m.Workspace)
	workerNotes := workeroutcomes.NewNotes(store, nil, nil, nil)
	m.Verification = sessionverification.New(store, m.Workspace, m.Transcript)
	m.Verification.Evidence = closeoutevidence.New(store)
	workerCancel := workerresults.NewGracefulCancel()
	workerCards := workerresults.NewCards(store, m.Transcript, nil)
	m.Research = researchwarm.New(store, m.Gate, m.Transcript.Append)
	turnCuration := curation.New(store, m.Workspace, m.Gate, m.Naming, m.Research)
	if svc != nil {
		var policy curation.Policy
		var providers curation.Providers
		if svc.Policy != nil {
			policy = svc.Policy
		}
		if svc.Registry != nil {
			providers = svc.Registry
		}
		turnCuration.SetModelSources(policy, providers)
	}
	m.PolicyIndex = policyindex.New(store, m.Workspace)
	m.SourceBriefs = sourcebrief.New(store, m.Workspace)
	m.Limits = sessionlimits.New(cfg, m.Workspace)
	turnSpend := spendguard.New(store, m.Limits, tracker)
	var router *llm.StaticModelRouter
	if svc != nil {
		router = svc.Router
	}
	var models loading.Models
	if svc != nil && svc.Registry != nil {
		models = svc.Registry
	}
	m.Profiles = profiles.New(store, &m.Catalog, m.Workspace)
	m.ToolPolicy = policyfacts.New(m.Profiles)
	m.Feedback = policyfeedback.New()
	workerState := workeroutcomes.NewState(workeroutcomes.StatePorts{Sessions: store, Verification: m.Verification, Workspace: m.Workspace, OverlayChanges: OverlayWorkspaceChangedPaths})
	m.Loading = loading.New(store, m.Workspace, router, models, tracker, nil, m.Profiles.EffectiveSkillsForProfile, workerState.ForSession)
	m.ToolPolicy.SetSources(store, m.Workspace, m.Feedback, m.Loading.Ledger)
	turnHistory := history.New(store, m.Gate, m.Workspace, m.Limits, router, m.Profiles.RecallAvailable)
	m.Admission = turnadmission.New(store, m.Gate, m.Workspace, turnTurns, m.queue)
	m.Submissions = submissions.New(store, m.queue, m.Gate, m.Recovery, turnSpend, m.Drafts, turnSubmissionState, &turnExecution.Prompt, m.Admission.RunLocked, m.Transcript.AppendContinuation, m.Admission.RoundComplete, turnTurns.HostTurnBlocked)
	if svc != nil {
		var policy sessionlimits.ModelPolicySource
		var windows llm.ContextLengthLookup
		if svc.Policy != nil {
			policy = svc.Policy
		}
		if svc.Registry != nil {
			windows = svc.Registry
		}
		m.Limits.SetModelSources(policy, windows)
	}
	m.Captures = checkpointcontrol.NewCapture(m.dataDir, store, m.Workspace)
	m.ToolContext = toolcontext.New(store, m.Workspace, m.Limits, m.Profiles, m.Captures, turnExecution)

	m.Rewinds = checkpointcontrol.NewRewinds(store, m.Captures, &turnExecution.Prompt, m.Workspace, m.projects, nil, m.events, checkpointcontrol.Runtime{
		WorkersInFlight: func(ctx context.Context, sess *api.Session) int {
			return workerState.ForSession(ctx, sess).WorkersInFlight
		},
		ResetWorkers: m.rollbackWorkerState, ResetCoordinator: m.rollbackCoordinatorBatch, ResetTurnLedgers: m.rollbackTurnLedgers, ResetProgress: m.rollbackProgress, ResetQueue: m.rollbackQueue,
	})
	m.Interruptions = execution.NewRecovery(store, m.Transcript, turnStatus, m.Rewinds)
	m.ensureResourceRegistry()
	m.Chats = chats.New(store, m.PolicyIndex, &turnExecution.Prompt, m.Captures, m.Gate, m.queue, m.resources)
	m.Stops = stopping.New(store, m.Gate, turnExecution, m.Chats, m.queue, m.Drafts, m.Interruptions, turnStatus)
	workerDigests := workeroutcomes.NewDigests()
	workerCancellations := workeroutcomes.NewCancellations(workeroutcomes.CancellationPorts{Sessions: store, Execution: turnExecution, Stops: m.Stops, Graceful: workerCancel, Cards: workerCards})
	workerDelivery := workeroutcomes.NewDeliveryPolicy(workeroutcomes.DeliveryPolicyPorts{Sessions: store, Profiles: m.Profiles, Facts: m.ToolPolicy, Feedback: m.Feedback})
	workerResults := workeroutcomes.NewResults(workeroutcomes.ResultPorts{Sessions: store, Limits: m.Limits, Cancellations: workerCancellations, Digests: workerDigests})
	workerSummaries := workeroutcomes.NewSummaries(workeroutcomes.SummaryPorts{Sessions: store, Limits: m.Limits, Budget: workerResults, Verification: m.Verification, Resources: m.Chats, Delivery: workerDelivery, Cards: workerCards, Digests: workerDigests, InspectChanges: InspectOverlayChanges, OverlayStatus: ResolveWorkerSummaryStatus, OverlayPending: OverlayAwaitingPromote})
	workerResults.SetSummaries(workerSummaries)
	m.Guidance = guidancedelivery.New(store, m.Transcript, workerState, workerResults, m.ProgressClosure)
	m.Processes = processcontrol.New(store, m.Verification, m.ToolPolicy, m.Guidance)
	m.Workers = workerexecution.New(store, m.Workspace, m.Gate, m.Guidance, m.Limits, turnHistory, router, workerexecution.PeerServices{Cancel: workerCancel, Cards: workerCards, Cancellations: workerCancellations, Delivery: workerDelivery, Summaries: workerSummaries, Results: workerResults, Digests: workerDigests, State: workerState, Notes: workerNotes, Workspaces: workerworkspace.New(store, m.Workspace, newBranchWorkspace)})
	turnPreparation := preparation.New(turnHistory, m.Loading, m.Profiles, m.SourceBriefs, m.ToolContext, m.Workers.Workspaces, m.Workers.State)
	turnPostTurn := postturn.New(store, m.Guidance)
	m.Batch = batchcontrol.New(store, workerState, m.Verification, m.Guidance)
	turnSettlement := turnsettlement.New(store, m.Gate, &turnExecution.Prompt, turnStatus, m.Workspace, m.Batch)
	m.Nudges = turnnudges.New(m.Loading.Ledger, workerResults, turnSpend)
	turnInstructions := instructions.New(store, m.Transcript, m.Captures, m.Guidance.DeliverPending)
	m.Runner = turnexecution.New(store, svc, turnexecution.Components{Execution: turnExecution, Workspace: m.Workspace, Spend: turnSpend, Transcript: m.Transcript, Turns: turnTurns, Chats: m.Chats, SubmissionState: turnSubmissionState, Settlement: turnSettlement, Clocks: turnClocks, Closeouts: turnCloseouts, Guidance: m.Guidance, Curation: turnCuration, Status: turnStatus, Instructions: turnInstructions, Preparation: turnPreparation, Authorization: turnAuthorization, History: turnHistory, PostTurn: turnPostTurn})
	m.Workers.Harness = workerharness.New(store, m.Profiles, m.ToolContext, m.Workers.Workspaces, m.Workspace, registry, m.Verification, OverlaySourceProof)
	m.Admission.Bind(m.Runner, turnSettlement, m.Submissions)
	m.Closeout = closeoutassembly.New(store, m.Workspace, workerState, m.Verification.Evidence)
	m.ProjectControl = projectcontrol.New(store, m.Stops, turnStatus, m.Admission, m.Batch, turnInstructions, m.Transcript)
	m.Guards = turnguards.New(store, m.ToolPolicy, m.Verification, m.Feedback, workerState, m.Workers, m.Workspace, m.Batch, m.ProgressClosure, turnCloseouts, m.Profiles, m.Limits)
	m.Loading.SetPolicy(m.Guards.Policy)
	runtime := m.ensureCoordinatorRuntime()
	m.Observations = sessionobservation.New(store, runtime.CoordinatorLoop(), m.Runner.Turns)
	m.Processes.SetLoop(runtime.CoordinatorLoop())
	m.Admission.SetLoop(runtime.CoordinatorLoop())
	m.ProjectControl.SetAnchors(runtime.Anchors())
	m.Nudges.SetSurface(runtime)
	m.Guards.SetSurface(runtime)
	m.Batch.SetLoop(runtime.CoordinatorLoop())
	turnSettlement.SetRuntime(runtime)
	m.Runner.SetRuntime(runtime)

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
