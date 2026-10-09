package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/promptresult"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkflowSessionView supplies session workflow operations.
type WorkflowSessionView interface {
	RecordReviewToolResult(context.Context, string, api.Message) error
	GetActive(ctx context.Context, sessionID string) (*api.WorkflowRun, error)
	IsAmbientRun(run *api.WorkflowRun) bool
	// Manifest-derived gate state.
	AssertSessionRunnable(ctx context.Context, sessionID string) error
	CurrentPhase(ctx context.Context, sessionID string) string
	ActivePhaseHasReviewLoop(ctx context.Context, sessionID string) bool
	ActivePhaseGuardState(ctx context.Context, sessionID string) WorkflowPhaseGuardState
	// ActiveReviewVerdictPending reports an unsatisfied review verdict.
	ActiveReviewVerdictPending(ctx context.Context, sessionID string) bool
	// ActiveCloseoutGateState reports a gated phase with open completion gates.
	ActiveCloseoutGateState(ctx context.Context, sessionID string) WorkflowCloseoutGateState
	AllowedAgents(ctx context.Context, sessionID string) []string
	ActiveManifest(ctx context.Context, sessionID string) (ActiveWorkflowManifest, bool)
	// ResolvedRequest returns the active run's resolved request, if available.
	ResolvedRequest(ctx context.Context, sessionID string) ResolvedWorkflowRequest
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

// WorkflowCloseoutGateState reports unresolved active closeout gates.
type WorkflowCloseoutGateState struct {
	Gated      bool
	Phase      string
	OpenLeaves []string
}

// WorkflowPhaseGuardState carries manifest-derived closeout and dispatch facts.
type WorkflowPhaseGuardState struct {
	Phase string
	// ReportPhaseDeclared marks the phase that produces the run report.
	ReportPhaseDeclared   bool
	ReportCloseoutPending bool
	// PendingObligationKinds identifies unsettled phase work.
	PhaseObligationPending bool
	PendingObligationKinds []string
}

// WorkerPhaseTouchPathsSource supplies manifest touch.paths for worker prompt inject.
type WorkerPhaseTouchPathsSource interface {
	PhaseTouchPaths(ctx context.Context, parentSessionID string) []string
}

// SetWorkflowSessionView wires the workflow session view for prompts, tools, and messages.
func (m *Manager) SetWorkflowSessionView(v WorkflowSessionView) {
	m.workflows = v
}

// AcceptsEmptyWorkflowRequest reports whether an empty prompt has active workflow semantics.
func (m *Manager) AcceptsEmptyWorkflowRequest(ctx context.Context, sessionID string) bool {
	if m == nil {
		return false
	}
	return m.workflows != nil && m.workflows.AcceptsEmptyRequest(ctx, sessionID)
}

func workflowEvaluationFromFrame(frame inject.CoordinatorTurnFrame) feedback.WorkflowEvaluationContext {
	runCtx := frame.RunContext
	out := feedback.WorkflowEvaluationContext{
		WorkflowID:         strings.TrimSpace(runCtx.WorkflowID),
		CurrentPhase:       strings.TrimSpace(runCtx.CurrentPhase),
		FailedLeaves:       append([]string(nil), runCtx.FailedLeaves...),
		RunActive:          strings.TrimSpace(runCtx.RunStatus) == string(api.WorkflowRunStatusRunning),
		AdvanceWhenGateMet: strings.TrimSpace(runCtx.AdvanceWhenGateMet),
	}
	if frame.Runtime.PhaseExit != nil {
		out.PhaseExitKind = frame.Runtime.PhaseExit.Kind
	}
	for _, phase := range frame.Runtime.Phases {
		if phase.ID != out.CurrentPhase {
			continue
		}
		out.CurrentGatesKnown = false
		out.CurrentGatesPassed = false
		out.FailedLeaves = nil
		for _, gate := range phase.Gates {
			if gate.Dormant {
				continue
			}
			out.CurrentGatesKnown = true
			if gate.Satisfied {
				continue
			}
			out.FailedLeaves = append(out.FailedLeaves, gate.ID)
		}
		out.CurrentGatesPassed = out.CurrentGatesKnown && len(out.FailedLeaves) == 0
		break
	}
	return out
}
