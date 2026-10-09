package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkflowSessionView supplies session workflow operations.
type WorkflowSessionView interface {
	GetActive(ctx context.Context, sessionID string) (*api.WorkflowRun, error)
	IsAmbientRun(run *api.WorkflowRun) bool
	// Manifest-derived gate state.
	AssertSessionRunnable(ctx context.Context, sessionID string) error
	CurrentPhase(ctx context.Context, sessionID string) string
	ActivePhaseHasReviewLoop(ctx context.Context, sessionID string) bool
	ActivePhaseGuardState(ctx context.Context, sessionID string) workflowfacts.WorkflowPhaseGuardState
	// ActiveReviewVerdictPending reports an unsatisfied review verdict.
	ActiveReviewVerdictPending(ctx context.Context, sessionID string) bool
	// ActiveCloseoutGateState reports a gated phase with open completion gates.
	ActiveCloseoutGateState(ctx context.Context, sessionID string) workflowfacts.WorkflowCloseoutGateState
	AllowedAgents(ctx context.Context, sessionID string) []string
	ActiveManifest(ctx context.Context, sessionID string) (workflowfacts.ActiveWorkflowManifest, bool)
	// ResolvedRequest returns the active run's resolved request, if available.
	ResolvedRequest(ctx context.Context, sessionID string) workflowfacts.ResolvedWorkflowRequest
	ParallelTaskMaxWorkers(ctx context.Context, sessionID string) int
	ParallelTaskMaxReadWorkers(ctx context.Context, sessionID string) int
	ParallelTaskMaxWriteWorkers(ctx context.Context, sessionID string) int
	PhaseTouchPaths(ctx context.Context, sessionID string) []string
	ScaffoldVarsForSession(ctx context.Context, sessionID string) (map[string]any, error)
	ActivePlan(ctx context.Context, sessionID string) (planID, content string, ok bool)
	// ActivePhaseRequiresEvidence checks the active phase's evidence gate.
	ActivePhaseRequiresEvidence(ctx context.Context, sessionID, evidenceType string) bool

	ApplyCoordinatorBatchEvent(ctx context.Context, sessionID string, ev batch.Event, eventSeq int) error

	// submissionID identifies the prompt operation and slash message.
	TrySlashPrompt(ctx context.Context, sessionID, text, submissionID string) (*promptresult.Result, bool, error)
	AcceptsEmptyRequest(ctx context.Context, sessionID string) bool
	PrepareUserRequest(ctx context.Context, sessionID, text string) (string, *promptresult.Result, bool, error)
	TryResolveUserFeedback(ctx context.Context, sessionID, messageID, authorPersonID, message string) error
	StampAndAppendMessages(ctx context.Context, sessionID string, msgs ...api.Message) error

	// AnnouncePendingAsk appends a pending ask card.
	AnnouncePendingAsk(ctx context.Context, sessionID string)

	// Workflow proof operations.
	RecordWorkerTerminalProof(ctx context.Context, sessionID, completingJobID, summaryStatus string) error
	RecordBoardOrientReady(ctx context.Context, sessionID, injectKey string) error
	ReconcileTurnCompletion(ctx context.Context, sessionID string) error
	MaybeDeliverTopologyReport(ctx context.Context, sessionID, messageID string) error

	// ReconcileOrphanedRuns closes abandoned running workflows.
	ReconcileOrphanedRuns(ctx context.Context, sessionID string) error

	// ForgetSession releases per-session guard state on session deletion.
	ForgetSession(sessionID string)
}

// workflowfacts.WorkflowCloseoutGateState reports unresolved active closeout gates.

// workflowfacts.WorkflowPhaseGuardState carries manifest-derived closeout and dispatch facts.

// WorkerPhaseTouchPathsSource supplies manifest touch.paths for worker prompt inject.
type WorkerPhaseTouchPathsSource interface {
	PhaseTouchPaths(ctx context.Context, parentSessionID string) []string
}

// SetWorkflowSessionView wires the workflow session view for prompts, tools, and messages.
func (m *Manager) SetWorkflowSessionView(v WorkflowSessionView) {
	m.workflows = v
	m.Closeout.SetWorkflow(v)
	m.Guards.SetWorkflow(v)
	m.Verification.Evidence.SetWorkflow(v)
	m.Runner.SetControl(v)
	m.Runner.SetRequests(v)
	m.Runner.SetSlash(v)
	m.Runner.Settlement.SetWorkflow(v)
	m.Batch.SetWorkflow(v)
	m.Guidance.SetWorkflow(v)
	m.Workers.State.SetWorkflows(v)
	m.Verification.SetWorkflow(v)
	m.Profiles.SetCoordinatorProfile(func(ctx context.Context, id string) string {
		if v == nil {
			return ""
		}
		manifest, ok := v.ActiveManifest(ctx, id)
		if !ok {
			return ""
		}
		return manifest.CoordinatorProfile
	})
	m.Runner.Instructions.SetWorkflow(v)
	m.Loading.SetWorkflow(v)
	m.Transcript.SetPageReconciler(v)
	m.Transcript.SetWorkflow(v)
}

// AcceptsEmptyWorkflowRequest reports whether an empty prompt has active workflow semantics.
func (m *Manager) AcceptsEmptyWorkflowRequest(ctx context.Context, sessionID string) bool {
	if m == nil {
		return false
	}
	return m.workflows != nil && m.workflows.AcceptsEmptyRequest(ctx, sessionID)
}
