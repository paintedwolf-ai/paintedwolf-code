package progress

import api "github.com/lycaon/lycaon/pkg/api"

func transitionMessages(before, after Snapshot) []api.Message {
	if before.Equal(after) {
		return nil
	}
	msg := api.Message{
		Role:          api.MessageRoleSystem,
		Visibility:    api.MessageVisibilityTranscript,
		WorkflowRunID: after.RunID,
	}
	if after.AllTerminal() && !before.AllTerminal() {
		msg.Kind = api.MessageKindProgressComplete
		msg.ProgressComplete = &api.ProgressCompleteMeta{Steps: after.Derive(DefaultProgressCap).Items}
	} else {
		meta, ok := buildProgressUpdate(before, after)
		if !ok {
			return nil
		}
		msg.Kind = api.MessageKindProgressUpdate
		msg.ProgressUpdate = meta
	}
	return []api.Message{msg}
}

func buildProgressUpdate(before, after Snapshot) (*api.ProgressUpdateMeta, bool) {
	if after.AllTerminal() && !before.AllTerminal() {
		return nil, false
	}
	changes := DiffSteps(before, after)
	if len(changes) == 0 {
		return nil, false
	}
	steps := after.Derive(DefaultProgressCap).Items
	summary := SummarizeUpdate(steps, len(changes))
	meta := &api.ProgressUpdateMeta{Summary: &summary}
	if len(before.Derive(DefaultProgressCap).Items) == 0 {
		meta.Initial = true
		if len(steps) <= MaxDetailedProgressUpdateRows {
			meta.Steps = steps
		}
	} else if len(changes) <= MaxDetailedProgressUpdateRows {
		meta.Changes = changes
	}
	return meta, true
}
