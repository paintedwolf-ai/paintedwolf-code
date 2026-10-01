package scan

import (
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

// ErrGatePending indicates closeout is waiting on scan evidence or an in-flight scan.
var ErrGatePending = errors.New("gate_pending")

// GatePendingError carries scan job context for gate_pending responses and SSE.
type GatePendingError struct {
	ScanID   string
	Guidance []api.ScanGuidanceSummary
	Reason   string
}

func (e *GatePendingError) Error() string {
	if e == nil {
		return "gate_pending"
	}
	if e.Reason != "" {
		return fmt.Sprintf("gate_pending: %s", e.Reason)
	}
	return "gate_pending"
}

func (e *GatePendingError) Is(target error) bool {
	return target == ErrGatePending
}

// AsGatePending unwraps a GatePendingError.
func AsGatePending(err error) (*GatePendingError, bool) {
	var pe *GatePendingError
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}
