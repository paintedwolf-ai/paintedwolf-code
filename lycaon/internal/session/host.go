package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/internal/session/authorization"
	"github.com/lycaon/lycaon/internal/session/batchcontrol"
	sessioncatalog "github.com/lycaon/lycaon/internal/session/catalog"
	"github.com/lycaon/lycaon/internal/session/chats"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	"github.com/lycaon/lycaon/internal/session/closeoutassembly"
	"github.com/lycaon/lycaon/internal/session/closeoutevidence"
	"github.com/lycaon/lycaon/internal/session/closeouts"
	"github.com/lycaon/lycaon/internal/session/coordinatorcontrol"
	"github.com/lycaon/lycaon/internal/session/curation"
	"github.com/lycaon/lycaon/internal/session/decisions"
	"github.com/lycaon/lycaon/internal/session/draftqueue"
	"github.com/lycaon/lycaon/internal/session/execution"
	"github.com/lycaon/lycaon/internal/session/guidancedelivery"
	"github.com/lycaon/lycaon/internal/session/history"
	"github.com/lycaon/lycaon/internal/session/instructions"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	sessionlimits "github.com/lycaon/lycaon/internal/session/limits"
	"github.com/lycaon/lycaon/internal/session/loading"
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
	"github.com/lycaon/lycaon/internal/session/resources"
	"github.com/lycaon/lycaon/internal/session/rewindruntime"
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
	"github.com/lycaon/lycaon/internal/session/workerexecution"
	"github.com/lycaon/lycaon/internal/session/workerharness"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/session/workerresults"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type Host struct {
	Resources      *resources.Service
	RewindRuntime  *rewindruntime.Service
	Coordinator    *coordinatorcontrol.Service
	ProjectControl *projectcontrol.Service
	Admission      *turnadmission.Service
	Processes      *processcontrol.Service
	Promotion      *promotionstate.Service
	Runner         *turnexecution.Service
	Stops          *stopping.Service
	ToolPolicy     *policyfacts.Service
	ToolContext    *toolcontext.Service
	Verification   *sessionverification.Service
	Profiles       *profiles.Service
	Workers        *workerexecution.Service
	Chats          *chats.Service
	Limits         *sessionlimits.Service
	SourceBriefs   *sourcebrief.Service
	Workspace      *sessionscope.Service
	Catalog        sessioncatalog.Service
	Observations   *sessionobservation.Service
	Submissions    *submissions.Service
	Decisions      decisions.Store
}

// SetLoopbackProvenance installs the provenance resolver for task container recording.
func (m *Host) SetLoopbackProvenance(p LoopbackProvenanceResolver) {
	if m != nil {

		m.ToolContext.SetContainers(p)
		m.Chats.SetLoopback(p)
	}
}

// NewHost acquires the session domains and binds their shared resources.
func NewHost(store Store, modelSources Models, registry tools.ToolRegistry) *Host {
	client, svc, cfg, tracker := modelSources.Client, modelSources.Provider, modelSources.Limits, modelSources.Cost

	turnAuthorization := authorization.New()
	queueStore := queue.New()
	gate := lifecycle.New(store)
	protection := protection.New()
	recovery := recovery.New(store)
	turnClocks := turnclock.New(store)
	m := &Host{
		Coordinator: &coordinatorcontrol.Service{Scans: &coordinatorcontrol.Scans{Sessions: store}},
		Promotion:   promotionstate.New(),
		Workspace:   sessionscope.New(store),
		Catalog:     sessioncatalog.New(store),
	}
	var namers naming.Namers
	if svc != nil && svc.Utility != nil {
		namers = svc
	}
	m.Coordinator.ProgressClosure = progressclosure.New()
	turnCloseouts := closeouts.New(store)
	turnExecution := execution.NewLifetime(gate)
	turnStatus := execution.NewStatus(store)
	turnTurns := execution.NewJournal(store)
	namingService := naming.New(store, m.Workspace, namers, tracker)
	turnSubmissionState := submissionstate.New(store, queueStore)
	drafts := draftqueue.New(store, queueStore, turnSubmissionState.Publish)
	turnTranscript := transcript.New(store, m.Workspace)
	workerNotes := workeroutcomes.NewNotes(store, nil, nil, nil)
	m.Verification = sessionverification.New(store, m.Workspace, turnTranscript)
	m.Verification.Evidence = closeoutevidence.New(store)
	workerCancel := workerresults.NewGracefulCancel()
	workerCards := workerresults.NewCards(store, turnTranscript, nil)
	research := researchwarm.New(store, gate, turnTranscript.Append)
	turnCuration := curation.New(store, m.Workspace, gate, namingService, research)
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
	m.Coordinator.PolicyIndex = policyindex.New(store, m.Workspace)
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
	m.Coordinator.Feedback = policyfeedback.New()
	workerState := workeroutcomes.NewState(workeroutcomes.StatePorts{Sessions: store, Verification: m.Verification, Workspace: m.Workspace, OverlayChanges: OverlayWorkspaceChangedPaths})
	m.Coordinator.Loading = loading.New(store, m.Workspace, router, models, tracker, nil, m.Profiles.EffectiveSkillsForProfile, workerState.ForSession)
	m.ToolPolicy.SetSources(store, m.Workspace, m.Coordinator.Feedback, m.Coordinator.Loading.Ledger)
	turnHistory := history.New(store, gate, m.Workspace, m.Limits, router, m.Profiles.RecallAvailable)
	m.Admission = turnadmission.New(store, gate, m.Workspace, turnTurns, queueStore)
	m.Submissions = submissions.New(store, queueStore, gate, recovery, turnSpend, drafts, turnSubmissionState, &turnExecution.Prompt, m.Admission.RunLocked, turnTranscript.AppendContinuation, m.Admission.RoundComplete, turnTurns.HostTurnBlocked)
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
	captures := checkpointcontrol.NewCapture(m.Workspace.DataDir, store, m.Workspace)
	m.ToolContext = toolcontext.New(store, m.Workspace, m.Limits, m.Profiles, captures, turnExecution)

	m.RewindRuntime = &rewindruntime.Service{Promotion: m.Promotion, Closeouts: turnCloseouts, ProgressClosure: m.Coordinator.ProgressClosure, History: turnHistory, Queue: queueStore, Drafts: drafts, Submissions: turnSubmissionState}
	rewinds := checkpointcontrol.NewRewinds(store, captures, &turnExecution.Prompt, m.Workspace, nil, nil, nil, checkpointcontrol.Runtime{
		WorkersInFlight: func(ctx context.Context, sess *api.Session) int {
			return workerState.ForSession(ctx, sess).WorkersInFlight
		},
		ResetWorkers: m.RewindRuntime.ResetWorkers, ResetCoordinator: m.RewindRuntime.ResetCoordinator, ResetTurnLedgers: m.RewindRuntime.ResetTurnLedgers, ResetProgress: m.RewindRuntime.ResetProgress, ResetQueue: m.RewindRuntime.ResetQueue,
	})
	interruptions := execution.NewRecovery(store, turnTranscript, turnStatus, rewinds)
	m.Resources = resources.New(&resources.Work{Execution: turnExecution, Curation: turnCuration, History: turnHistory.Runner, Research: research}, &resources.TurnState{Capture: &captures.Capture, Streams: turnTranscript.Streams, ProgressClosure: m.Coordinator.ProgressClosure, Closeouts: turnCloseouts, Spend: turnSpend, Promotion: m.Promotion, History: turnHistory, Gate: gate, PolicyIndex: m.Coordinator.PolicyIndex, Protection: protection}, &resources.ToolState{}, &resources.Authority{}, queueStore)
	m.Chats = chats.New(store, m.Coordinator.PolicyIndex, &turnExecution.Prompt, queueStore, m.Resources.Registry, chats.Lifecycle{Gate: gate, Captures: captures, Naming: namingService, Research: research, Protection: protection, Recovery: recovery, Rewinds: rewinds, Drafts: drafts})
	m.Stops = stopping.New(store, gate, turnExecution, m.Chats, queueStore, drafts, interruptions, turnStatus)
	workerDigests := workeroutcomes.NewDigests()
	workerCancellations := workeroutcomes.NewCancellations(workeroutcomes.CancellationPorts{Sessions: store, Execution: turnExecution, Stops: m.Stops, Graceful: workerCancel, Cards: workerCards})
	workerDelivery := workeroutcomes.NewDeliveryPolicy(workeroutcomes.DeliveryPolicyPorts{Sessions: store, Profiles: m.Profiles, Facts: m.ToolPolicy, Feedback: m.Coordinator.Feedback})
	workerResults := workeroutcomes.NewResults(workeroutcomes.ResultPorts{Sessions: store, Limits: m.Limits, Cancellations: workerCancellations, Digests: workerDigests})
	workerSummaries := workeroutcomes.NewSummaries(workeroutcomes.SummaryPorts{Sessions: store, Limits: m.Limits, Budget: workerResults, Verification: m.Verification, Resources: m.Chats, Delivery: workerDelivery, Cards: workerCards, Digests: workerDigests, InspectChanges: InspectOverlayChanges, OverlayStatus: ResolveWorkerSummaryStatus, OverlayPending: OverlayAwaitingPromote})
	workerResults.SetSummaries(workerSummaries)
	m.Coordinator.Guidance = guidancedelivery.New(store, turnTranscript, workerState, workerResults, m.Coordinator.ProgressClosure)
	m.Processes = processcontrol.New(store, m.Verification, m.ToolPolicy, m.Coordinator.Guidance)
	m.Workers = workerexecution.New(store, m.Workspace, gate, m.Coordinator.Guidance, m.Limits, turnHistory, router, workerexecution.PeerServices{Cancel: workerCancel, Cards: workerCards, Cancellations: workerCancellations, Delivery: workerDelivery, Summaries: workerSummaries, Results: workerResults, Digests: workerDigests, State: workerState, Notes: workerNotes, Workspaces: workerworkspace.New(store, m.Workspace, newBranchWorkspace)})
	turnPreparation := preparation.New(turnHistory, m.Coordinator.Loading, m.Profiles, m.SourceBriefs, m.ToolContext, m.Workers.Workspaces, m.Workers.State)
	turnPostTurn := postturn.New(store, m.Coordinator.Guidance)
	m.Coordinator.Batch = batchcontrol.New(store, workerState, m.Verification, m.Coordinator.Guidance)
	turnSettlement := turnsettlement.New(store, gate, &turnExecution.Prompt, turnStatus, m.Workspace, m.Coordinator.Batch)
	m.Coordinator.Nudges = turnnudges.New(m.Coordinator.Loading.Ledger, workerResults, turnSpend)
	turnInstructions := instructions.New(store, turnTranscript, captures, m.Coordinator.Guidance.DeliverPending)
	m.Runner = turnexecution.New(store, svc, turnexecution.Components{Execution: turnExecution, Workspace: m.Workspace, Spend: turnSpend, Transcript: turnTranscript, Turns: turnTurns, Chats: m.Chats, SubmissionState: turnSubmissionState, Settlement: turnSettlement, Clocks: turnClocks, Closeouts: turnCloseouts, Guidance: m.Coordinator.Guidance, Curation: turnCuration, Status: turnStatus, Instructions: turnInstructions, Preparation: turnPreparation, Authorization: turnAuthorization, History: turnHistory, PostTurn: turnPostTurn})
	m.Workers.Harness = workerharness.New(store, m.Profiles, m.ToolContext, m.Workers.Workspaces, m.Workspace, registry, m.Verification, OverlaySourceProof)
	m.Admission.Bind(m.Runner, turnSettlement, m.Submissions)
	m.Coordinator.Closeout = closeoutassembly.New(store, m.Workspace, workerState, m.Verification.Evidence)
	m.ProjectControl = projectcontrol.New(store, m.Stops, turnStatus, m.Admission, m.Coordinator.Batch, turnInstructions, turnTranscript)
	m.Coordinator.Guards = turnguards.New(store, m.ToolPolicy, m.Verification, m.Coordinator.Feedback, workerState, m.Workers, m.Workspace, m.Coordinator.Batch, m.Coordinator.ProgressClosure, turnCloseouts, m.Profiles, m.Limits)
	m.Coordinator.Loading.SetPolicy(m.Coordinator.Guards.Policy)
	m.Resources.State.Settlement = turnSettlement
	m.Resources.State.Batch = m.Coordinator.Batch
	m.RewindRuntime.Settlement = turnSettlement
	m.RewindRuntime.Batch = m.Coordinator.Batch
	m.RewindRuntime.Touches = m.Workers.Workspaces.Touches
	m.acquireCoordinatorSources(store, client, svc, registry, tracker)
	runtime := coordinator.NewRuntime(m.Coordinator.RuntimeDependencies())
	m.Coordinator.Runtime = runtime
	m.Stops.SetCoordinator(runtime)
	m.Coordinator.Guidance.Bind(runtime.Kicks(), runtime.Anchors())
	m.ToolPolicy.SetSurface(runtime)
	m.Coordinator.Context.Runtime = runtime
	m.Coordinator.Tools.Runtime = runtime
	m.Coordinator.Control.Runtime = runtime
	m.Coordinator.Assembly.Runtime = runtime
	m.Coordinator.Loop.Runtime = runtime
	m.Resources.Work.Coordinator = runtime
	m.RewindRuntime.Coordinator = runtime
	m.Observations = sessionobservation.New(store, runtime.CoordinatorLoop(), m.Runner.Turns)
	m.Processes.SetLoop(runtime.CoordinatorLoop())
	m.Admission.SetLoop(runtime.CoordinatorLoop())
	m.ProjectControl.SetAnchors(runtime.Anchors())
	m.Coordinator.Nudges.SetSurface(runtime)
	m.Coordinator.Guards.SetSurface(runtime)
	m.Coordinator.Batch.SetLoop(runtime.CoordinatorLoop())
	turnSettlement.SetRuntime(runtime)
	m.Runner.SetRuntime(runtime)

	m.Coordinator.Admission = m.Admission
	m.Coordinator.Runtime = runtime
	m.Coordinator.Workers = &coordinatorcontrol.Workers{Runtime: runtime, Batch: m.Coordinator.Batch, Settlement: turnSettlement, Admission: m.Admission, Digests: workerDigests, Results: workerResults}
	m.Coordinator.Scans.Runtime = runtime

	m.Coordinator.Profiles = m.Profiles

	m.Coordinator.ToolPolicy = m.ToolPolicy

	return m
}

// Models binds the provider and accounting policy shared by turn and worker execution.
type Models struct {
	Client   modelcall.LLMClient
	Provider *llm.Service
	Limits   settings.SessionLimits
	Cost     cost.CostTracker
}
