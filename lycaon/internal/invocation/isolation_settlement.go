package invocation

import (
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

func receiptIsolation(outcome isolation.Outcome) *api.InvocationIsolation {
	return &api.InvocationIsolation{Code: outcome.Code, Disposition: string(outcome.Disposition)}
}

func validateIsolationSettlement(in Settlement) error {
	var boundary *isolation.Outcome
	if in.Isolation != nil {
		outcome, ok := isolation.Lookup(strings.TrimSpace(in.Isolation.Code))
		if !ok || string(outcome.Disposition) != strings.TrimSpace(in.Isolation.Disposition) {
			return fmt.Errorf("isolation settlement has unregistered code/disposition: code=%q disposition=%q", in.Isolation.Code, in.Isolation.Disposition)
		}
		boundary = &outcome
	}
	var rejection isolation.Outcome
	registered := false
	if in.Failure != nil {
		rejection, registered = isolation.Lookup(strings.TrimSpace(in.Failure.Code))
		if registered != (in.Failure.Class == api.FailureClassIsolationRejection) {
			return fmt.Errorf("isolation settlement code/class mismatch: code=%q class=%q", in.Failure.Code, in.Failure.Class)
		}
	}
	if !registered {
		if boundary != nil && !boundary.Retryable() {
			return fmt.Errorf("terminal isolation outcome %q requires its rejection failure", boundary.Code)
		}
		return nil
	}
	if boundary == nil || boundary.Code != rejection.Code {
		return fmt.Errorf("isolation failure %q is not bound to its receipt outcome", in.Failure.Code)
	}
	if in.Failure.Retryable != rejection.Retryable() {
		return fmt.Errorf("isolation settlement retryability mismatch for %q", in.Failure.Code)
	}
	// Invoked stays the owner's fact: a review the owner raises at its effect
	// seam stops an operation that already ran.
	if !rejection.Retryable() && in.Status != api.InvocationStatusRejected {
		return fmt.Errorf("terminal isolation outcome %q requires a rejected receipt", rejection.Code)
	}
	return nil
}
