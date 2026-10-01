package workflow

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/visual"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// Manager controls WorkflowRun lifecycle and AssertRunnable gating.
type Manager interface {
	ValidateUserFacingStart(ctx context.Context, projectDir, sessionID, workflowID, version string) error
	Start(ctx context.Context, sessionID string, req api.StartWorkflowRunRequest) (*api.WorkflowRun, error)
	Exit(ctx context.Context, sessionID, runID string, expectedRevision int64, reason string) (*api.WorkflowRun, error)
	Get(ctx context.Context, runID string) (*api.WorkflowRun, error)
	GetActive(ctx context.Context, sessionID string) (*api.WorkflowRun, error)
	SyncHumanApproval(ctx context.Context, runID, projectDir string) (*api.WorkflowRun, error)
	Pause(ctx context.Context, runID, reason string) (*api.WorkflowRun, error)
	Resume(ctx context.Context, runID string) (*api.WorkflowRun, error)
	Cancel(ctx context.Context, runID, reason string) (*api.WorkflowRun, error)
	Advance(ctx context.Context, runID string) (*api.WorkflowRun, error)
	FireTransition(ctx context.Context, runID, transitionID, actor string) (*api.WorkflowRun, error)
	ResolveUserDecision(ctx context.Context, sessionID, runID, phaseID string, choices []string, comment string) (*api.WorkflowRun, error)
	ResolveUserFeedback(ctx context.Context, sessionID, runID, phaseID, response string) (*api.WorkflowRun, error)
	ResolveUserSecret(ctx context.Context, sessionID, runID, phaseID, value string) (*api.WorkflowRun, error)
	MarkTopologyStageComplete(ctx context.Context, runID, stage, output, designForkCriterion string) error
	AssertRunnable(ctx context.Context, runID string) error
	// ManifestForRunID and ListReviewLoopEvidence back the run report; the
	// report is unassemblable without them.
	ManifestForRunID(ctx context.Context, runID string) (workflowdef.Manifest, error)
	ReportAvailable(ctx context.Context, runID string) (bool, error)
	ListReviewLoopEvidence(ctx context.Context, sessionID, workflowRunID, phaseSlot string) ([]evidence.Record, error)
}

// BlueprintCreator drafts blueprint files for supporting workflow runs.
type BlueprintCreator interface {
	CreateBlueprint(ctx context.Context, projectID, title, path, sourceWorkflowID string) (blueprintPath string, err error)
	ResolveDraft(ctx context.Context, projectID, blueprintPath string) error
	RetargetToTitle(ctx context.Context, projectID, path, title string) (string, error)
}

// RunManager implements Manager against RunStore.
type RunManager struct {
	Store           RunStore
	Sessions        session.Store
	Manifests       *workflowdef.Registry
	Resolver        ManifestResolver
	Events          *events.Publisher
	BlueprintCreate BlueprintCreator
	BlueprintGet    BlueprintGetter
	Gates           GateEvaluator
	Registry        *conditions.ConditionRegistry
	Obligations     map[string]ObligationKind
	TopologyLegs    TopologyLegSource
	EvidenceDigests []EvidenceDigestSource
	WorkerTasks     func(context.Context, string) ([]api.WorkerTask, error)
	// WorkerToolBudget bounds planned leg ceilings for a project root.
	WorkerToolBudget    func(projectDir string) spawn.WorkerToolBudget
	SessionScaffold     SessionScaffoldStore
	OnPhaseAutoAdvanced PhaseAutoAdvancedHook
	OnRunResumed        func(context.Context, *api.WorkflowRun)
	OnRunCompleted      func(context.Context, *api.WorkflowRun)
	// OnRequestAccepted curates a newly committed workflow request after admission.
	OnRequestAccepted       func(context.Context, string, string)
	OnHumanApprovalAdvanced HumanApprovalAdvancedHook
	OnFeedbackPending       FeedbackPendingHook
	// OnToolAskOpened reports a committed coordinator-tool ask.
	OnToolAskOpened ToolAskOpenedHook
	// OnFeedbackResolved reports cleared pending input.
	OnFeedbackResolved     FeedbackResolvedHook
	OnReviewLoopHeld       ReviewLoopHeldHook
	PhaseEnterHook         PhaseEnterHook
	PhaseReenterHook       PhaseReenterHook
	WorkerStop             WorkerRunStop
	SessionCoordinatorBusy SessionCoordinatorBusy
	SessionAdmission       sessionStartAdmission
	SessionExit            sessionExitControl
	// EvidenceStore persists review verdicts as anchored gate records.
	EvidenceStore inspector.EvidenceStore
	// EvidenceProjectDir resolves the session's project directory for evidence records.
	EvidenceProjectDir func(ctx context.Context, sessionID string) (string, error)
	// OnGateEvidencePersisted projects persisted review verdicts.
	OnGateEvidencePersisted func(ctx context.Context, sessionID, workflowRunID string, rec evidence.Record)
	// ReviewSpawnFilter selects spawnable reviewers; nil requires every candidate.
	ReviewSpawnFilter func(ctx context.Context, sessionID, surfaceID string, candidates []string) []string
	// VerdictGrounding checks terminal review citations against the sojourn evidence.
	VerdictGrounding func(ctx context.Context, sessionID string, cited []api.CitationGroundingCitedEvidence, citedURLs, owedAgents []string) (guidance.VerdictGroundingEval, error)
	// Inventory reads the scanner groups bound to a run, so cited group ids are
	// checked where they are submitted.
	Inventory ScanInventory
	// OrphanReconcileBefore is this process's boot: runs created before it, in
	// sessions with no turn progress since, are orphan candidates.
	OrphanReconcileBefore time.Time

	// Artifacts stores visual attachments for the session tree.
	Artifacts visual.Store
	// RootSessionID resolves a session to its tree root.
	RootSessionID func(ctx context.Context, sessionID string) string
	SecretCapture SecretCapture

	// startGuards serializes active-run creation per session.
	startGuards sync.Map // sessionID -> *sync.Mutex

	// askUserGuards serializes ask lifecycle changes per session.
	askUserGuards sync.Map // sessionID -> *sync.Mutex

	// runVarsGuards serializes scaffold-variable updates per run.
	runVarsGuards sync.Map // runID -> *sync.Mutex
}

type sessionExitControl interface {
	WithSessionTreeStop(context.Context, string, string, func(context.Context) error) error
}

type sessionStartAdmission interface {
	WithSessionTreeAdmission(ctx context.Context, sessionID string, fn func() error) error
}

// FeedbackPendingHook is invoked when a phase sets user_feedback pending on enter.
type FeedbackPendingHook func(ctx context.Context, sessionID, phaseID string)

// ToolAskOpenedHook is invoked after ask_user / RequestUserInput opens a tool ask.
type ToolAskOpenedHook func(ctx context.Context, sessionID, phaseID string)

// FeedbackResolvedHook is invoked when pending user feedback/decision is cleared.
type FeedbackResolvedHook func(ctx context.Context, sessionID, runID, phaseID, response string)

// ReviewLoopHeldHook reports a held review and whether its cap requires a terminal verdict.
type ReviewLoopHeldHook func(ctx context.Context, sessionID string, decisionRequired bool)

// PhaseAutoAdvancedHook is invoked after host auto-advance commits a new phase.
type PhaseAutoAdvancedHook func(ctx context.Context, sessionID, runID, previousPhase, newPhase string)

// HumanApprovalAdvancedHook reports executable work after an approval transition.
type HumanApprovalAdvancedHook func(ctx context.Context, run *api.WorkflowRun)

// NewManager creates a workflow run manager.
func NewManager(store RunStore, sessions session.Store, manifests *workflowdef.Registry, pub *events.Publisher) *RunManager {
	if manifests == nil {
		manifests = workflowdef.NewRegistry(nil)
	}
	manager := &RunManager{
		Store:     store,
		Sessions:  sessions,
		Manifests: manifests,
		Events:    pub,
		Gates:     FailClosedGateEvaluator{},
	}
	if setter, ok := store.(interface{ SetSessionMutations(atomicSessionMutations) }); ok {
		if mutations, ok := sessions.(atomicSessionMutations); ok {
			setter.SetSessionMutations(mutations)
		}
	}
	return manager
}

// SetVisualArtifacts sets the session-tree artifact store.
func (m *RunManager) SetVisualArtifacts(store visual.Store, rootSessionID func(ctx context.Context, sessionID string) string) {
	if m == nil {
		return
	}
	m.Artifacts = store
	m.RootSessionID = rootSessionID
}

func (m *RunManager) treeRoot(ctx context.Context, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if m != nil && m.RootSessionID != nil {
		if root := strings.TrimSpace(m.RootSessionID(ctx, sessionID)); root != "" {
			return root
		}
	}
	return sessionID
}

func (m *RunManager) projectDir(ctx context.Context, sessionID string) string {
	if m == nil {
		return ""
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	if m.Sessions != nil {
		if sess, err := m.Sessions.Get(ctx, sessionID); err == nil && sess != nil && strings.TrimSpace(sess.WorkspacePath) != "" {
			return strings.TrimSpace(sess.WorkspacePath)
		}
	}
	if m.EvidenceProjectDir != nil {
		if dir, err := m.EvidenceProjectDir(ctx, sessionID); err == nil && strings.TrimSpace(dir) != "" {
			return strings.TrimSpace(dir)
		}
	}
	return ""
}

// SetConditionRegistry sets the manifest gate evaluator.
func (m *RunManager) SetConditionRegistry(reg *conditions.ConditionRegistry) {
	if m == nil {
		return
	}
	m.Registry = reg
	if reg != nil {
		m.Gates = RegistryGateEvaluator{Registry: reg, Sessions: m.Sessions}
	}
}

func (m *RunManager) gateEvaluator() GateEvaluator {
	if m != nil && m.Gates != nil {
		return m.Gates
	}
	return FailClosedGateEvaluator{}
}

func (m *RunManager) Get(ctx context.Context, runID string) (*api.WorkflowRun, error) {
	run, err := m.Store.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	return run, nil
}

// GetActive returns the unadorned active leaf for a session.
func (m *RunManager) GetActive(ctx context.Context, sessionID string) (*api.WorkflowRun, error) {
	return m.Store.ActiveBySession(ctx, sessionID)
}

// AssertRunnable rejects paused, terminal, or missing runs for prompt/tool/dispatch paths.
func (m *RunManager) AssertRunnable(ctx context.Context, runID string) error {
	if runID == "" {
		return nil
	}
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return err
	}
	switch run.Status {
	case api.WorkflowRunStatusRunning:
		return nil
	case api.WorkflowRunStatusPaused:
		return &NotRunnableError{RunID: runID, Status: run.Status, Reason: "paused"}
	case api.WorkflowRunStatusPausedOnChild:
		return &NotRunnableError{RunID: runID, Status: run.Status, Reason: "paused_on_child"}
	case api.WorkflowRunStatusCanceled:
		return &NotRunnableError{RunID: runID, Status: run.Status, Reason: "canceled"}
	case api.WorkflowRunStatusComplete:
		return &NotRunnableError{RunID: runID, Status: run.Status, Reason: "complete"}
	default:
		return &NotRunnableError{RunID: runID, Status: run.Status, Reason: string(run.Status)}
	}
}

func (m *RunManager) loadRun(ctx context.Context, runID string) (*api.WorkflowRun, error) {
	run, err := m.Store.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	return run, nil
}

var _ Manager = (*RunManager)(nil)

// startGuardFor returns the session's start mutex.
func (m *RunManager) startGuardFor(sessionID string) *sync.Mutex {
	if v, ok := m.startGuards.Load(sessionID); ok {
		return v.(*sync.Mutex)
	}
	mu := &sync.Mutex{}
	actual, _ := m.startGuards.LoadOrStore(sessionID, mu)
	return actual.(*sync.Mutex)
}

// askUserGuardFor returns the session's pending-input mutex.
func (m *RunManager) askUserGuardFor(sessionID string) *sync.Mutex {
	if v, ok := m.askUserGuards.Load(sessionID); ok {
		return v.(*sync.Mutex)
	}
	mu := &sync.Mutex{}
	actual, _ := m.askUserGuards.LoadOrStore(sessionID, mu)
	return actual.(*sync.Mutex)
}

// ForgetSession releases guards after session deletion stops further runs and questions.
func (m *RunManager) ForgetSession(sessionID string) {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	m.startGuards.Delete(sessionID)
	m.askUserGuards.Delete(sessionID)
}

// lockRunVars serializes one run's variable changes.
func (m *RunManager) lockRunVars(runID string) func() {
	v, ok := m.runVarsGuards.Load(runID)
	if !ok {
		v, _ = m.runVarsGuards.LoadOrStore(runID, &sync.Mutex{})
	}
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// lockRunVarsOnce returns an idempotent unlock so advanceToNextPhase can
// release before phase-enter stamps take the same lock.
func (m *RunManager) lockRunVarsOnce(runID string) func() {
	unlock := m.lockRunVars(runID)
	var once sync.Once
	return func() { once.Do(unlock) }
}

// WorkerRunStop cancels or holds workers and delegations when a workflow run stops.
// Implemented by worker.RunStopService in production wiring.
type WorkerRunStop interface {
	CancelWorkersByRunID(ctx context.Context, runID, reason string) error
	SettleWorkerCancellationsByRunID(ctx context.Context, runID, reason string) error
	HoldPendingWorkersByRunID(ctx context.Context, runID string) error
	CancelDelegationsByRunID(ctx context.Context, runID string) error
}
