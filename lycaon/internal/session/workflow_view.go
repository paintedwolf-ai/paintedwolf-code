package session

import (
	workflowfacts "github.com/lycaon/lycaon/internal/session/workflowfacts"

	"context"
	"github.com/lycaon/lycaon/internal/promptresult"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkflowDomains binds the workflow resources consumed by session execution.
type WorkflowDomains struct {
	Runs       WorkflowRuns
	Policy     WorkflowPolicy
	Ambient    WorkflowAmbient
	Blueprints WorkflowBlueprints
	Batch      WorkflowBatch
	Slash      WorkflowSlash
	Requests   WorkflowRequests
	Feedback   WorkflowFeedback
	Transcript WorkflowTranscript
	Asks       WorkflowAsks
	Fanout     WorkflowFanout
	Phases     WorkflowPhases
	Reports    WorkflowReports
	Recovery   WorkflowRecovery
	Cleanup    WorkflowCleanup
	Reviews    WorkflowReviews
}

func (d *WorkflowDomains) RecordReviewToolResult(ctx context.Context, sessionID string, msg api.Message) error {
	if d == nil || d.Reviews == nil {
		return nil
	}
	return d.Reviews.RecordReviewToolResult(ctx, sessionID, msg)
}

type WorkflowReviews interface {
	RecordReviewToolResult(context.Context, string, api.Message) error
}

type WorkflowRuns interface {
	ActiveBySession(context.Context, string) (*api.WorkflowRun, error)
}

type WorkflowPolicy interface {
	AssertSessionRunnable(ctx context.Context, sessionID string) error
	CurrentPhase(ctx context.Context, sessionID string) string
	ActivePhaseHasReviewLoop(ctx context.Context, sessionID string) bool
	ActivePhaseGuardState(ctx context.Context, sessionID string) workflowfacts.WorkflowPhaseGuardState
	ActiveReviewVerdictPending(ctx context.Context, sessionID string) bool
	ActiveCloseoutGateState(ctx context.Context, sessionID string) workflowfacts.WorkflowCloseoutGateState
	AllowedAgents(ctx context.Context, sessionID string) []string
	ActiveManifest(ctx context.Context, sessionID string) (workflowfacts.ActiveWorkflowManifest, bool)
	ResolvedRequest(ctx context.Context, sessionID string) workflowfacts.ResolvedWorkflowRequest
	ScaffoldVarsForSession(ctx context.Context, sessionID string) (map[string]any, error)
	ActivePhaseRequiresEvidence(ctx context.Context, sessionID, evidenceType string) bool
}

type WorkflowAmbient interface {
	ParallelTaskMaxWorkers(ctx context.Context, sessionID string) int
	ParallelTaskMaxReadWorkers(ctx context.Context, sessionID string) int
	ParallelTaskMaxWriteWorkers(ctx context.Context, sessionID string) int
	PhaseTouchPaths(ctx context.Context, sessionID string) []string
}

type WorkflowBlueprints interface {
	ActivePlan(ctx context.Context, sessionID string) (planID, content string, ok bool)
}

type WorkflowBatch interface {
	ApplyCoordinatorBatchEvent(ctx context.Context, sessionID string, ev batch.Event, eventSeq int) error
}

type WorkflowSlash interface {
	TrySlashPrompt(ctx context.Context, sessionID, text, submissionID string) (*promptresult.Result, bool, error)
}

type WorkflowRequests interface {
	AcceptsEmptyRequest(ctx context.Context, sessionID string) bool
	PrepareUserRequest(ctx context.Context, sessionID, text string) (string, *promptresult.Result, bool, error)
}

type WorkflowFeedback interface {
	TryResolveUserFeedback(ctx context.Context, sessionID, messageID, authorPersonID, message string) error
}

type WorkflowTranscript interface {
	StampAndAppendMessages(ctx context.Context, sessionID string, msgs ...api.Message) error
}

type WorkflowAsks interface {
	AnnouncePendingAsk(ctx context.Context, sessionID string)
}

type WorkflowFanout interface {
	RecordWorkerTerminalProof(ctx context.Context, sessionID, completingJobID, summaryStatus string) error
	RecordBoardOrientReady(ctx context.Context, sessionID, injectKey string) error
}

type WorkflowPhases interface {
	ReconcileTurnCompletion(ctx context.Context, sessionID string) error
}

type WorkflowReports interface {
	MaybeDeliverTopologyReport(ctx context.Context, sessionID, messageID string) error
}

type WorkflowRecovery interface {
	ReconcileOrphanedRuns(ctx context.Context, sessionID string) error
}

type WorkflowCleanup interface {
	ForgetSession(sessionID string)
}

// WorkerPhaseTouchPathsSource supplies manifest touch.paths for worker prompt inject.
type WorkerPhaseTouchPathsSource interface {
	PhaseTouchPaths(ctx context.Context, parentSessionID string) []string
}

// SetWorkflowDomains wires the workflow resources for prompts, tools, and messages.
func (m *Manager) SetWorkflowDomains(v *WorkflowDomains) {
	m.workflows = v
}

// AcceptsEmptyWorkflowRequest reports whether an empty prompt has active workflow semantics.
func (m *Manager) AcceptsEmptyWorkflowRequest(ctx context.Context, sessionID string) bool {
	if m == nil {
		return false
	}
	return m.workflows != nil && m.workflows.Requests.AcceptsEmptyRequest(ctx, sessionID)
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
