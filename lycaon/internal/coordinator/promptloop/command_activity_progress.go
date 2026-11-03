package promptloop

import "github.com/lycaon/lycaon/pkg/api"

// Command edges publish immediately; unchanged counters use normal coalescing.
func commandActivityEdge(previous, next *api.ToolProgress) bool {
	if next == nil || next.CommandProgram == nil {
		return false
	}
	if previous == nil || previous.CommandProgram == nil {
		return len(next.CommandProgram.Activities) > 0
	}
	before := previous.CommandProgram
	after := next.CommandProgram
	type edge struct {
		state           string
		failed, waiting int
	}
	states := make(map[string]edge, len(before.Activities))
	for _, activity := range before.Activities {
		states[activity.ID] = edge{activity.State, activity.FailedCount, activity.WaitingCount}
	}
	for _, activity := range after.Activities {
		if states[activity.ID] != (edge{activity.State, activity.FailedCount, activity.WaitingCount}) {
			return true
		}
	}
	return false
}
