package loopwake

import (
	awaitstore "github.com/lycaon/lycaon/internal/await"
)

func (l *WaitSubscriptions) processConditionOutcome(sessionID string, condition awaitstore.Condition) (awaitstore.Condition, bool) {
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
