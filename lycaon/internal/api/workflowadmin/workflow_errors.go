package workflowadmin

import (
	"errors"
	"net/http"

	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// workflowFailure is the answer for one workflow sentinel error.
type workflowFailure struct {
	sentinel error
	code     wire.ApiErrorCode
	message  string
}

var workflowFailures = []workflowFailure{
	{store.ErrSessionNotFound, wire.ApiErrorCodeSessionNotFound, "chat not found"},
	{runstate.ErrNotFound, wire.ApiErrorCodeWorkflowRunNotFound, "workflow run not found"},
	{workflowdef.ErrUnknownWorkflow, wire.ApiErrorCodeWorkflowNotFound, "workflow not found"},
	{runstate.ErrActiveRunExists, wire.ApiErrorCodeWorkflowActive, "exit the active workflow run before starting another"},
	{lifecycle.ErrStopping, wire.ApiErrorCodeSessionStopping, "chat is stopping"},
	{runstate.ErrNoActiveRun, wire.ApiErrorCodeWorkflowRunNotFound, "no active workflow run"},
	{runstate.ErrRevisionConflict, wire.ApiErrorCodeWorkflowRevisionConflict, "workflow run changed; reload it"},
	{runstate.ErrOperationConflict, wire.ApiErrorCodeIdempotencyConflict, "operation_id was already used for a different request"},
	{runstate.ErrWorkflowReplacementTargetRequired, wire.ApiErrorCodeWorkflowReplacementTargetRequired, "name the active run this workflow replaces"},
	{runstate.ErrPlanNotFound, wire.ApiErrorCodeBlueprintNotFound, "blueprint not found"},
	{runstate.ErrPlanNotDraft, wire.ApiErrorCodeBlueprintNotDraft, "blueprint is not a draft"},
	{runstate.ErrWorkflowStartRequiresHumanApproval, wire.ApiErrorCodeWorkflowStartRequiresHumanApproval, "this workflow starts only with human approval"},
	{runstate.ErrPlanDraftRequired, wire.ApiErrorCodeBlueprintDraftRequired, "a draft blueprint is required"},
	{runstate.ErrHumanApprovalNotReady, wire.ApiErrorCodeHumanApprovalNotReady, "human approval is not ready"},
	{runstate.ErrFeedbackNotPending, wire.ApiErrorCodeFeedbackNotPending, "no feedback is pending"},
	{runstate.ErrFeedbackEmptyResponse, wire.ApiErrorCodeFeedbackResponseRequired, "feedback response is required"},
	{runstate.ErrDecisionNotPending, wire.ApiErrorCodeDecisionNotPending, "no decision is pending"},
	{runstate.ErrNotChoicePhase, wire.ApiErrorCodeNotChoicePhase, "the current phase is not a choice"},
	{runstate.ErrDecisionChoiceInvalid, wire.ApiErrorCodeDecisionChoiceInvalid, "choice is not offered by this decision"},
	{runstate.ErrInvalidTransition, wire.ApiErrorCodeInvalidWorkflowTransition, "workflow transition is not allowed"},
	{runstate.ErrTransitionUnknown, wire.ApiErrorCodeChoiceTransitionNotFound, "transition not found on the current phase"},
	{runstate.ErrTransitionActorDenied, wire.ApiErrorCodeChoiceTransitionActorDenied, "this transition is not yours to fire"},
	{runstate.ErrTransitionNotArmed, wire.ApiErrorCodeChoiceTransitionNotArmed, "transition is not armed"},
	{runstate.ErrTransitionPendingInput, wire.ApiErrorCodeChoiceTransitionPendingInput, "transition is waiting for input"},
}

// writeRunLookupError answers a failed read of one workflow run.
func (s *RunControl) writeRunLookupError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, runstate.ErrNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeWorkflowRunNotFound, "workflow run not found")
		return
	}
	s.responses.InternalError(w, r, err)
}

func (s *RunControl) WriteWorkflowError(w http.ResponseWriter, r *http.Request, err error) {
	// A run's missing pinned version is more specific than an unknown workflow.
	var unavailable *runstate.WorkflowVersionUnavailableError
	if errors.As(err, &unavailable) {
		s.responses.FailDetails(w, wire.ApiErrorCodeWorkflowVersionUnavailable,
			map[string]any{"workflow_id": unavailable.WorkflowID, "version": unavailable.Version},
			"this run's workflow version is no longer available")
		return
	}
	for _, failure := range workflowFailures {
		if errors.Is(err, failure.sentinel) {
			s.responses.Fail(w, failure.code, failure.message)
			return
		}
	}
	var gateErr *runstate.PhaseGateUnmetError
	var notRunnable *runstate.NotRunnableError
	switch {
	case errors.Is(err, workflowdef.ErrWorkflowParameterInvalid):
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest,
			map[string]any{"field": "parameters", "reason": "a workflow parameter or preset is invalid"},
			"a workflow parameter or preset is invalid")
	case errors.As(err, &gateErr):
		details := map[string]any{
			"phase":  gateErr.Phase,
			"reason": gateErr.Reason,
		}
		if gateErr.FailedGate != "" {
			details["failed_gate"] = gateErr.FailedGate
		}
		if len(gateErr.FailedLeaves) > 0 {
			details["failed_leaves"] = gateErr.FailedLeaves
		}
		s.responses.FailDetails(w, wire.ApiErrorCodePhaseGateUnmet, details, "phase gate is not met")
	case errors.As(err, &notRunnable):
		details := map[string]any{"run_id": notRunnable.RunID, "status": string(notRunnable.Status)}
		if notRunnable.Reason != "" {
			details["reason"] = notRunnable.Reason
		}
		s.responses.FailDetails(w, wire.ApiErrorCodeWorkflowNotRunnable, details, "workflow run is not runnable")
	default:
		s.responses.InternalError(w, r, err)
	}
}
