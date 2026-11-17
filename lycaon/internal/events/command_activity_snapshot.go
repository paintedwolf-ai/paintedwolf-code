package events

import (
	"slices"

	"github.com/lycaon/lycaon/internal/actioncard"
	"github.com/lycaon/lycaon/pkg/api"
)

// describeActivityProgram attaches each running command activity's card. A
// progress event carries no arguments, so activities show their subjects.
func describeActivityProgram(ev api.ActivityEvent) api.ActivityEvent {
	if ev.Progress == nil || ev.Progress.CommandProgram == nil {
		return ev
	}
	progress := *ev.Progress
	program := cloneCommandActivityProgram(progress.CommandProgram)
	call := api.ToolResult{Tool: ev.ToolName, ToolCallID: ev.ToolCallID}
	for i := range program.Activities {
		action := actioncard.ForActivity(*program, program.Activities[i], call, nil)
		program.Activities[i].Action = &action
	}
	progress.CommandProgram = program
	ev.Progress = &progress
	return ev
}

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
		activity.Steps = slices.Clone(activity.Steps)
		activity.StatusCode = cloneActivityStatus(activity.StatusCode)
		if activity.Action != nil {
			action := *activity.Action
			activity.Action = &action
		}
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
