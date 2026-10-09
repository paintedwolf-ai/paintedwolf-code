package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/closeoutassembly"
	"github.com/lycaon/lycaon/internal/session/guidancedelivery"
	"github.com/lycaon/lycaon/internal/session/instructions"
	"github.com/lycaon/lycaon/internal/session/promptsource"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/session/turnguards"
	"github.com/lycaon/lycaon/internal/session/turnsettlement"
	workflowfacts "github.com/lycaon/lycaon/internal/session/workflowfacts"
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

type workflowManifestAdapter struct {
	policy WorkflowPolicy
}

func (a workflowManifestAdapter) ActiveManifest(ctx context.Context, sessionID string) (assembly.ActiveWorkflowManifest, bool) {
	if a.policy == nil {
		return assembly.ActiveWorkflowManifest{}, false
	}
	m, ok := a.policy.ActiveManifest(ctx, sessionID)
	if !ok {
		return assembly.ActiveWorkflowManifest{}, false
	}
	return assembly.ActiveWorkflowManifest{
		CoordinatorProfile: m.CoordinatorProfile,
		Archive:            m.Archive,
	}, true
}

// SetWorkflowDomains wires the workflow resources for prompts, tools, and messages.
func (m *Host) SetWorkflowDomains(v *WorkflowDomains) {

	m.Coordinator.Control.Workflow = nil
	m.Coordinator.Projection.Workflow = nil
	m.Coordinator.Projection.Reviews = nil
	m.Coordinator.Assembly.Workflow = promptsource.AssemblyWorkflow{}
	m.Coordinator.Loop.ActiveRuns = nil
	if v != nil {
		m.Coordinator.Control.Workflow = v.Policy
		m.Coordinator.Projection.Workflow = v.Asks
		m.Coordinator.Projection.Reviews = v.Reviews
		var manifests assembly.WorkflowManifestSource
		if v.Policy != nil {
			manifests = workflowManifestAdapter{policy: v.Policy}
		}
		m.Coordinator.Assembly.Workflow = promptsource.AssemblyWorkflow{Manifests: manifests, Orientation: v.Fanout}
		m.Coordinator.Loop.ActiveRuns = v.Runs
	}
	if v == nil {
		m.Resources.Work.Workflow = nil
	} else {
		m.Resources.Work.Workflow = v.Cleanup
	}
	if v == nil {
		m.Coordinator.Closeout.SetWorkflow(nil)
		m.Verification.Evidence.SetWorkflow(nil)
		m.Runner.Settlement.SetWorkflow(nil)
		m.Coordinator.Guards.SetWorkflow(nil)
		m.Runner.Transcript.SetWorkflow(nil)
		m.Verification.SetWorkflow(nil)
		m.Coordinator.Batch.SetWorkflow(nil)
		m.Coordinator.Guidance.SetWorkflow(nil)
		m.Coordinator.Loading.SetWorkflow(nil)
		m.Runner.Instructions.SetWorkflow(nil)
		m.Runner.SetControl(nil)
		m.Runner.SetRequests(nil)
		m.Runner.SetSlash(nil)
		m.Workers.State.SetWorkflows(nil)
		m.Runner.Transcript.SetPageReconciler(nil)
		m.Profiles.SetCoordinatorProfile(nil)
		return
	}
	m.Coordinator.Closeout.SetWorkflow(&closeoutassembly.WorkflowDomains{Policy: v.Policy, Runs: v.Runs})
	m.Verification.Evidence.SetWorkflow(v.Runs)
	m.Runner.Settlement.SetWorkflow(&turnsettlement.WorkflowDomains{Phases: v.Phases, Policy: v.Policy, Reports: v.Reports})
	m.Coordinator.Guards.SetWorkflow(&turnguards.WorkflowDomains{Ambient: v.Ambient, Blueprints: v.Blueprints, Policy: v.Policy, Runs: v.Runs})
	m.Runner.Transcript.SetWorkflow(&transcript.WorkflowDomains{Feedback: v.Feedback, Transcript: v.Transcript})
	m.Verification.SetWorkflow(v.Policy)
	m.Coordinator.Batch.SetWorkflow(v.Batch)
	m.Coordinator.Guidance.SetWorkflow(&guidancedelivery.WorkflowDomains{Policy: v.Policy, Runs: v.Runs})
	m.Coordinator.Loading.SetWorkflow(v.Policy)
	m.Runner.Instructions.SetWorkflow(&instructions.WorkflowDomains{Batch: v.Batch, Feedback: v.Feedback, Runs: v.Runs})
	m.Runner.SetControl(v.Policy)
	m.Runner.SetRequests(v.Requests)
	m.Runner.SetSlash(v.Slash)
	m.Workers.State.SetWorkflows(v.Policy)
	m.Runner.Transcript.SetPageReconciler(v.Recovery)
	policy := v.Policy
	m.Profiles.SetCoordinatorProfile(func(ctx context.Context, id string) string {
		if policy == nil {
			return ""
		}
		manifest, ok := policy.ActiveManifest(ctx, id)
		if !ok {
			return ""
		}
		return manifest.CoordinatorProfile
	})
}

// AcceptsEmptyWorkflowRequest reports whether an empty prompt has active workflow semantics.

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
