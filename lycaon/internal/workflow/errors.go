package workflow

import (
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/noticeerr"
	"github.com/lycaon/lycaon/pkg/api"
)

var (
	// ErrRunNotFound is returned when a workflow run id does not exist.
	ErrRunNotFound = errors.New("workflow run not found")
	// ErrActiveRunExists is returned when starting a run while one is already active.
	ErrActiveRunExists error = noticeerr.NewSentinel("active workflow run already exists", api.NoticeCodeWorkflowActive)
	// ErrRunRevisionConflict identifies stale mutation state.
	ErrRunRevisionConflict = errors.New("workflow run revision conflict")
	// ErrOperationConflict reports an operation identity (HTTP operation_id, or a
	// tool call id acting as one) replayed against a receipt written for different
	// input. Every operation-identity surface wraps this so one HTTP mapping and
	// one tool rejection cover the whole family.
	ErrOperationConflict = errors.New("operation id was already used for different input")
	// ErrBlueprintApprovalConflict identifies bytes other than those reviewed.
	ErrBlueprintApprovalConflict = errors.New("blueprint approval content conflict")
	// ErrWorkflowReplacementTargetRequired requires a reviewed lineage target.
	ErrWorkflowReplacementTargetRequired = errors.New("workflow replacement target required")
	// ErrInvalidTransition is returned for illegal state transitions.
	ErrInvalidTransition = errors.New("invalid workflow transition")
	// ErrNoActiveRun is returned when no active workflow run exists for the session.
	ErrNoActiveRun = errors.New("no active workflow run")
	// ErrCoverageScansPending reports coverage facts asked for before the run's
	// bound scans settled; verdict admission and reports each answer it their way.
	ErrCoverageScansPending = errors.New("coverage scans have not settled")
	// ErrPlanNotDraft is returned when resuming a plan that is not in draft status.
	ErrPlanNotDraft = errors.New("plan is not draft")
	// ErrPlanNotFound is returned when a requested plan id does not exist.
	ErrPlanNotFound = errors.New("plan not found")
	// ErrBlueprintLaunchIncompatible is returned when the source path or target
	// workflow is not a valid blueprint launch pair.
	ErrBlueprintLaunchIncompatible = errors.New("blueprint launch target incompatible")
	// ErrBlueprintLaunchUnsupported is returned when the target lacks a blueprint: block.
	ErrBlueprintLaunchUnsupported = errors.New("blueprint launch target unsupported")
	// ErrSessionWorkflowNotFound is returned when a session-tier manifest row is missing.
	ErrSessionWorkflowNotFound = errors.New("session workflow not found")
	// ErrFeedbackNotPending is returned when feedback resolve targets a non-pending phase.
	ErrFeedbackNotPending = errors.New("feedback not pending for phase")
	// ErrFeedbackEmptyResponse is returned when feedback resolve has an empty response.
	ErrFeedbackEmptyResponse = errors.New("feedback response required")
	// ErrDecisionNotPending is returned when a choice resolve targets a non-pending phase
	// (e.g. a stale or double-submitted card).
	ErrDecisionNotPending = errors.New("decision not pending for phase")
	// ErrNotChoicePhase is returned when a choice resolve targets a non-choice phase.
	ErrNotChoicePhase = errors.New("phase is not a choice phase")
	// ErrDecisionChoiceInvalid is returned when a choice is not among the phase options and the
	// phase does not allow an "Other" answer, or the selection count is wrong for the type.
	ErrDecisionChoiceInvalid = errors.New("choice not valid for phase")
	// ErrWorkflowStartRequiresHumanApproval is returned when coordinator state_start lacks approval.
	ErrWorkflowStartRequiresHumanApproval = errors.New("workflow start requires human approval")
	// ErrHumanApprovalNotReady is returned when SyncHumanApproval / host Yes runs while
	// human_approval readiness is false (no force-ready override).
	ErrHumanApprovalNotReady = errors.New("human_approval not ready")
	// ErrPlanDraftRequired is returned when a plan workflow cannot link a draft at start.
	ErrPlanDraftRequired = errors.New("plan draft required at workflow start")
	// ErrInheritedBlueprintNotApproved is returned when an invoked workflow would
	// mutate against Blueprint bytes that the user did not approve.
	ErrInheritedBlueprintNotApproved = errors.New("inherited blueprint is not approved")
	// ErrTransitionUnknown is returned when FireTransition cannot resolve the edge.
	ErrTransitionUnknown = errors.New("unknown choice transition")
	// ErrTransitionActorDenied is returned when the caller is not listed on the edge.
	ErrTransitionActorDenied = errors.New("choice transition actor denied")
	// ErrTransitionNotArmed is returned when optional when: readiness is false.
	ErrTransitionNotArmed = errors.New("choice transition not armed")
	// ErrTransitionPendingInput is returned when a feedback or decision latch is open.
	ErrTransitionPendingInput = errors.New("choice transition blocked by pending user input")
)

// NotRunnableError indicates the run cannot accept prompts, tools, or dispatch.
type NotRunnableError struct {
	RunID  string
	Status api.WorkflowRunStatus
	Reason string
}

func (e *NotRunnableError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("workflow not runnable: %s", e.Reason)
	}
	return "workflow not runnable"
}

func IsNotRunnable(err error) (*NotRunnableError, bool) {
	var nr *NotRunnableError
	if errors.As(err, &nr) {
		return nr, true
	}
	return nil, false
}

// NoticeCode reports the workflow-not-runnable notice.
func (e *NotRunnableError) NoticeCode() api.NoticeCode {
	return api.NoticeCodeWorkflowNotRunnable
}
