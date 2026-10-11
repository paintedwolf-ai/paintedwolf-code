package loopwake

import (
	"fmt"

	awaitstore "github.com/lycaon/lycaon/internal/await"
)

func validateCompletionWait(loop *WaitSubscriptions, sessionID string, conditions []awaitstore.Condition) error {
	state := loop.loopDeps().ProcessState
	if state == nil {
		return fmt.Errorf("process state is unavailable")
	}
	if len(conditions) == 0 {
		return fmt.Errorf("until_complete requires process_done conditions with exact handles")
	}
	for _, condition := range conditions {
		if condition.Kind != "process_done" || len(condition.Handles) == 0 {
			return fmt.Errorf("until_complete requires process_done conditions with exact handles")
		}
		for _, handle := range condition.Handles {
			if known, _ := state(sessionID, handle); !known {
				return fmt.Errorf("process handle %q is unavailable in this session", handle)
			}
		}
	}
	return nil
}

// State reconciliation waits for the published completion digest before settling.
