package loopwake

import (
	"fmt"

	awaitstore "github.com/lycaon/lycaon/internal/await"
)

func validateCompletionWait(loop *LoopEngine, sessionID string, conditions []awaitstore.Condition) error {
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
func (l *LoopEngine) processConditionOutcome(sessionID string, condition awaitstore.Condition) (awaitstore.Condition, bool) {
	deps := l.loopDeps()
	for _, handle := range condition.Handles {
		known, running := false, false
		if deps.ProcessState != nil {
			known, running = deps.ProcessState(sessionID, handle)
		}
		if known && running {
			continue
		}
		condition.Handles = []string{handle}
		if !known {
			condition.Outcome = "unavailable"
			return condition, true
		}
		if deps.ProcessReport != nil {
			report, published := deps.ProcessReport(sessionID, handle)
			if !published {
				continue
			}
			condition.Report = report
		}
		condition.Outcome = "satisfied"
		return condition, true
	}
	return awaitstore.Condition{}, false
}
