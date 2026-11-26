package actionoutcome

import "github.com/lycaon/lycaon/pkg/api"

func WakeResult(reason api.WakeReason) Result {
	r := Result{State: api.ContinuationStateNotContinued}
	switch reason {
	case api.WakeReasonHumanApprovalAwaiting, api.WakeReasonHostObligationHeld, api.WakeReasonPendingUserInput, api.WakeReasonActiveHold:
		return Result{State: api.ContinuationStateHeld, Reason: api.ContinuationReason(reason)}
	case api.WakeReasonNoRequest:
		r.Reason = api.ContinuationReasonNoOpenRequest
	case api.WakeReasonBudgetExhausted:
		r.Reason = api.ContinuationReasonTurnBudgetReached
	case api.WakeReasonNoActiveRun, api.WakeReasonWorkflowTerminal:
		r.Reason = api.ContinuationReasonRunInactive
	case api.WakeReasonSessionClosed, api.WakeReasonSessionMissing:
		r.Reason = api.ContinuationReasonSessionClosed
	case api.WakeReasonLoopDisabled, api.WakeReasonWrongSessionRole, api.WakeReasonEscalated, api.WakeReasonPolicyDenied, api.WakeReasonNonActionable:
		r.Reason = api.ContinuationReasonAdmissionDenied
	case api.WakeReasonPromptInterrupted, api.WakeReasonUserStopped, api.WakeReasonRewound:
		r.Reason = api.ContinuationReasonUserInterrupted
	case api.WakeReasonStaleBatch, api.WakeReasonPrecedenceBusy, api.WakeReasonJobTerminal, api.WakeReasonTurnSettled, api.WakeReasonBatchClosed, api.WakeReasonTurnRearm:
		r.Reason = api.ContinuationReasonActionInterrupted
	case api.WakeReasonLookupFailed:
		return Result{State: api.ContinuationStateFailed, FailureCode: string(api.HostContinuationFailureHostLookupFailed)}
	default:
		return Result{State: api.ContinuationStateFailed, FailureCode: string(api.HostContinuationFailureHostInvariantFailed)}
	}
	return r
}
