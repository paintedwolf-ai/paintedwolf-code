package orchestration

import (
	"fmt"
	"strings"
)

func effectiveFanOutProfile(spec FanOutSpec) string {
	profileID := strings.TrimSpace(spec.ProfileID)
	if profileID == "" {
		return ProfilePathExplorer
	}
	return profileID
}

func effectiveFanOutMaxWorkers(spec FanOutSpec) int {
	if spec.MaxWorkers > 0 {
		return spec.MaxWorkers
	}
	return len(spec.Subtasks)
}

func effectiveFanOutAggregation(spec FanOutSpec) AggregationMode {
	if spec.Aggregation == "" {
		return AggregationMerge
	}
	return spec.Aggregation
}

// validateFanOutSpec checks fan-out topology configuration at load and run time.
func validateFanOutSpec(spec FanOutSpec) error {
	if len(spec.Subtasks) == 0 {
		return fmt.Errorf("subtasks required")
	}
	for i, subtask := range spec.Subtasks {
		if strings.TrimSpace(subtask) == "" {
			return fmt.Errorf("subtask %d is empty", i)
		}
	}
	maxWorkers := effectiveFanOutMaxWorkers(spec)
	if len(spec.Subtasks) > maxWorkers {
		return fmt.Errorf("subtasks (%d) exceed max_workers (%d)", len(spec.Subtasks), maxWorkers)
	}
	switch effectiveFanOutAggregation(spec) {
	case AggregationUnion, AggregationIntersect, AggregationVote, AggregationMerge:
		return nil
	default:
		return fmt.Errorf("unknown aggregation %q", spec.Aggregation)
	}
}

func fanOutSubtaskKey(index int) string {
	return fmt.Sprintf("subtask-%d", index)
}
