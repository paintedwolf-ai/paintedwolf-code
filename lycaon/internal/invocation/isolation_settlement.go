package invocation

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/pkg/api"
)

// SettlementIsolation selects the isolation outcome a receipt records.
//
// An isolation rejection records its own outcome. Otherwise the receipt records
// the first observed retry outcome: terminal outcomes settle only as
// rejections, so a terminal refusal observed inside an attempted effect stays
// structured result feedback and never binds the receipt.
func SettlementIsolation(failure *api.InvocationFailure, observed []string) *api.InvocationIsolation {
	if failure != nil && failure.Class == api.FailureClassIsolationRejection {
		if outcome, ok := isolation.Lookup(strings.TrimSpace(failure.Code)); ok {
			return receiptIsolation(outcome)
		}
	}
	for _, code := range observed {
		if outcome, ok := isolation.Lookup(strings.TrimSpace(code)); ok && outcome.Retryable() {
			return receiptIsolation(outcome)
		}
	}
	return nil
}

// Isolation settlement refusals. Each names the rule a settlement broke.
var (
	ErrIsolationUnregistered         = errors.New("isolation settlement has unregistered code/disposition")
	ErrIsolationClassMismatch        = errors.New("isolation settlement code/class mismatch")
	ErrIsolationRejectionMissing     = errors.New("terminal isolation outcome requires its rejection failure")
	ErrIsolationUnbound              = errors.New("isolation failure is not bound to its receipt outcome")
	ErrIsolationRetryabilityMismatch = errors.New("isolation settlement retryability mismatch")
	ErrIsolationTerminalNotRejected  = errors.New("terminal isolation outcome requires a rejected receipt")
)

func receiptIsolation(outcome isolation.Outcome) *api.InvocationIsolation {
	return &api.InvocationIsolation{Code: outcome.Code, Disposition: string(outcome.Disposition)}
}

func validateIsolationSettlement(in Settlement) error {
	var boundary *isolation.Outcome
	if in.Isolation != nil {
		outcome, ok := isolation.Lookup(strings.TrimSpace(in.Isolation.Code))
		if !ok || string(outcome.Disposition) != strings.TrimSpace(in.Isolation.Disposition) {
			return fmt.Errorf("%w: code=%q disposition=%q", ErrIsolationUnregistered, in.Isolation.Code, in.Isolation.Disposition)
		}
		boundary = &outcome
	}
	var rejection isolation.Outcome
	registered := false
	if in.Failure != nil {
		rejection, registered = isolation.Lookup(strings.TrimSpace(in.Failure.Code))
		if registered != (in.Failure.Class == api.FailureClassIsolationRejection) {
			return fmt.Errorf("%w: code=%q class=%q", ErrIsolationClassMismatch, in.Failure.Code, in.Failure.Class)
		}
	}
	if !registered {
		if boundary != nil && !boundary.Retryable() {
			return fmt.Errorf("%w: %q", ErrIsolationRejectionMissing, boundary.Code)
		}
		return nil
	}
	if boundary == nil || boundary.Code != rejection.Code {
		return fmt.Errorf("%w: %q", ErrIsolationUnbound, in.Failure.Code)
	}
	if in.Failure.Retryable != rejection.Retryable() {
		return fmt.Errorf("%w: %q", ErrIsolationRetryabilityMismatch, in.Failure.Code)
	}
	// Invoked stays the owner's fact: a review the owner raises at its effect
	// seam stops an operation that already ran.
	if !rejection.Retryable() && in.Status != api.InvocationStatusRejected {
		return fmt.Errorf("%w: %q", ErrIsolationTerminalNotRejected, rejection.Code)
	}
	return nil
}
