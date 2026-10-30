package events

import (
	"slices"

	"github.com/lycaon/lycaon/pkg/api"
)

func cloneCommandActivityProgram(input *api.CommandProgramReceipt) *api.CommandProgramReceipt {
	if input == nil {
		return nil
	}
	result := *input
	result.Activities = slices.Clone(input.Activities)
	for i := range result.Activities {
		activity := &result.Activities[i]
		activity.ExecutionIDs = slices.Clone(activity.ExecutionIDs)
		activity.Spans = slices.Clone(activity.Spans)
		activity.StatusCode = cloneActivityStatus(activity.StatusCode)
	}
	result.ActivityExecutions = slices.Clone(input.ActivityExecutions)
	for i := range result.ActivityExecutions {
		result.ActivityExecutions[i].StatusCode = cloneActivityStatus(result.ActivityExecutions[i].StatusCode)
	}
	result.Outputs = slices.Clone(input.Outputs)
	for i := range result.Outputs {
		if result.Outputs[i].Limit != nil {
			limit := *result.Outputs[i].Limit
			result.Outputs[i].Limit = &limit
		}
	}
	result.StatusCode = cloneActivityStatus(input.StatusCode)
	return &result
}

func cloneActivityStatus(input *int) *int {
	if input == nil {
		return nil
	}
	result := *input
	return &result
}
