package promptloop

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/pkg/api"
)

// HostFaultError ends a turn whose host could not record a tool call's
// outcome. The call's result and receipt are durable before the turn ends.
type HostFaultError struct {
	Tool string
	// CallRan is the owner's invoked fact for the call.
	CallRan bool
	Cause   error
}

func (e *HostFaultError) Error() string {
	return fmt.Sprintf("host could not record the %s call's outcome: %v", e.Tool, e.Cause)
}

func (e *HostFaultError) Unwrap() error { return e.Cause }

// NoticeCode routes the turn failure to the host-fault notice.
func (e *HostFaultError) NoticeCode() api.NoticeCode { return api.NoticeCodeHostFault }

// NoticeHostFault supplies the notice's call facts.
func (e *HostFaultError) NoticeHostFault() (tool string, callRan bool) { return e.Tool, e.CallRan }

// settlementHostFault replaces a call's result with the host fault that kept
// its outcome from the ledger. The owner's content is withheld: it has not
// crossed the delivery policy that a recorded result crosses.
func (l *PromptLoop) settlementHostFault(tc api.ToolCall, assistantMessageID string, run toolInvocation, cause error) singleToolOutcome {
	fault := &HostFaultError{Tool: tc.Name, CallRan: run.invoked, Cause: cause}
	var receipt *api.InvocationReceipt
	var refused *invocation.SettlementRefusedError
	if errors.As(cause, &refused) && refused.Receipt != nil {
		receipt = refused.Receipt
		fault.CallRan = receipt.Invoked
	}
	reject := l.toolReject(invocation.SettlementRefusedCode, map[string]any{"tool": tc.Name})
	// The copy renders as a refusal; the call failed rather than being refused.
	facts := reject.Facts
	facts.Outcome = api.ToolResultOutcomeError
	msg := l.toolResultMessage(tc.Name, tc.ID, assistantMessageID, tc.Args, reject.Body, facts)
	msg.ToolResult.Invocation = receipt
	return singleToolOutcome{toolName: tc.Name, toolMsg: msg, endTurn: fault}
}

// batchHostFault joins the host faults of a concurrent batch's results.
func batchHostFault(outcomes []toolCallOutcome) error {
	var faults []error
	for _, out := range outcomes {
		if out.endTurn != nil {
			faults = append(faults, out.endTurn)
		}
	}
	return errors.Join(faults...)
}

// closeBatchForHostFault settles the calls a host fault kept from running and
// ends the turn with that fault.
func (l *PromptLoop) closeBatchForHostFault(
	ctx context.Context, sess *api.Session, sessionID, assistantID string,
	history []api.Message, calls []api.ToolCall, turnTools []string,
	lastToolTS *time.Time, st *promptLoopTurnState, fault error,
) ([]api.Message, []string, error) {
	history, turnTools, err := l.settleUnattemptedCalls(ctx, sess, sessionID, assistantID, history, calls, turnTools, lastToolTS, st)
	return history, turnTools, errors.Join(fault, err)
}
