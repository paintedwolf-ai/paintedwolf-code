package invocation

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

// SettlementRefusedCode is the failure a receipt carries when the host could
// not record the outcome its caller stated.
const SettlementRefusedCode = "INVOCATION_SETTLEMENT_REFUSED"

// EvidenceKindHostFault marks a receipt the host settled in place of its caller.
const EvidenceKindHostFault = "host_fault"

// SettlementRefusedError reports a settlement the ledger refused. Receipt is
// the host-fault settlement recorded in its place, nil when that could not be
// written either.
type SettlementRefusedError struct {
	ReceiptID string
	Receipt   *api.InvocationReceipt
	Violation error
}

func (e *SettlementRefusedError) Error() string {
	return fmt.Sprintf("invocation receipt %q settlement refused: %v", e.ReceiptID, e.Violation)
}

func (e *SettlementRefusedError) Unwrap() error { return e.Violation }

// refuseSettlement closes the receipt as a host fault. The owner's invoked
// fact stands; the stated outcome is what the host could not record.
func (r *SQLRecorder) refuseSettlement(ctx context.Context, id string, invoked bool, violation error) error {
	receipt, err := r.writeSettlement(ctx, id, Settlement{
		Status: api.InvocationStatusError, Invoked: invoked, EvidenceKind: EvidenceKindHostFault,
		Failure: &api.InvocationFailure{Code: SettlementRefusedCode, Class: api.FailureClassHostFault},
	})
	if err != nil {
		violation = errors.Join(violation, fmt.Errorf("record host fault: %w", err))
	}
	return &SettlementRefusedError{ReceiptID: id, Receipt: receipt, Violation: violation}
}
