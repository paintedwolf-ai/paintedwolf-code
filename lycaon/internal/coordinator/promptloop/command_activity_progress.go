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
	if before.ActivitiesOmitted == 0 && after.ActivitiesOmitted > 0 {
		return true
	}
	states := make(map[string]string, len(before.Activities))
	for _, activity := range before.Activities {
		states[activity.ID] = activity.State + "|" + activity.Outcome
	}
	for _, activity := range after.Activities {
		if states[activity.ID] != activity.State+"|"+activity.Outcome {
			return true
		}
	}
	return false
}
