package workflowfacts

import (
	"fmt"
	"strings"
)

// IncompleteCloseoutError retains the unmet host-owned completion requirements.
type IncompleteCloseoutError struct{ State WorkflowCloseoutGateState }

func (e *IncompleteCloseoutError) Error() string {
	return fmt.Sprintf("workflow phase %s remains incomplete: %s", e.State.Phase, strings.Join(e.State.OpenLeaves, ", "))
}
