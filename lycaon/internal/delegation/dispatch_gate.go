package delegation

import "context"

// DispatchGate blocks delegation dispatch on file overlap or circular depends_on.
type DispatchGate interface {
	Check(ctx context.Context, delegationID, legID string) (allowed bool, reason string, err error)
}

// AllowGate permits every dispatch. It is the base of the gate chain; the
// real constraints compose over it (WorkflowDispatchGate, PlanApprovalDispatchGate).
type AllowGate struct{}

// Check always allows dispatch.
func (AllowGate) Check(context.Context, string, string) (bool, string, error) {
	return true, "", nil
}
