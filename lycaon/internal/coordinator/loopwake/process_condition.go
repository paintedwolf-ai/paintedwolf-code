package loopwake

import (
	"fmt"
	"strings"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/watch"
)

func validateCompletionWait(loop *WaitSubscriptions, sessionID string, conditions []awaitstore.Condition) error {
	if len(conditions) == 0 {
		return fmt.Errorf("until_complete requires process_done or watch_match conditions with exact handles")
	}
	state := loop.loopDeps().ProcessState
	for _, condition := range conditions {
		switch condition.Kind {
		case "process_done":
			if state == nil {
				return fmt.Errorf("process state is unavailable")
			}
			if len(condition.Handles) == 0 {
				return fmt.Errorf("until_complete requires process_done or watch_match conditions with exact handles")
			}
			for _, handle := range condition.Handles {
				if known, _ := state(sessionID, handle); !known {
					return fmt.Errorf("process handle %q is unavailable in this session", handle)
				}
			}
		case watch.ConditionWatchMatch:
			if len(condition.Handles) == 0 {
				return fmt.Errorf("until_complete requires process_done or watch_match conditions with exact handles")
			}
		default:
			return fmt.Errorf("until_complete requires process_done or watch_match conditions with exact handles")
		}
	}
	return nil
}

func processDisplaySubject(loop *WaitSubscriptions, sessionID string, handles []string) string {
	subject := loop.loopDeps().ProcessSubject
	if subject == nil {
		return ""
	}
	for _, handle := range handles {
		if line := strings.TrimSpace(subject(sessionID, handle)); line != "" {
			return line
		}
	}
	return ""
}
