package messageview

import (
	"github.com/lycaon/lycaon/internal/actioncard"
	"github.com/lycaon/lycaon/pkg/api"
)

// TranscriptMessage hides tool-step prose without changing recorded history.
func TranscriptMessage(msg api.Message) api.Message {
	out := projectFileEdits(RedactMessage(msg))
	if out.Role == api.MessageRoleAssistant && len(out.ToolCalls) > 0 {
		out.Content = ""
		out.ContentParts = nil
		out.Grounding = nil
		out.NavigationRefs = nil
		out.SourceContext = nil
	}
	return describeActions(projectContent(out))
}

// describeActions attaches each action's card. It runs on every projection,
// so stored rows never carry descriptors.
func describeActions(msg api.Message) api.Message {
	if len(msg.ToolCalls) > 0 {
		calls := append([]api.ToolCall(nil), msg.ToolCalls...)
		for i := range calls {
			calls[i].Action = actioncard.ForCall(calls[i])
		}
		msg.ToolCalls = calls
	}
	if msg.ToolResult == nil {
		return msg
	}
	result := *msg.ToolResult
	marks := fieldMarks(msg.HostSecretRedaction, "tool_result.tool_args.command")
	if program := result.CommandProgram; program != nil {
		described := *program
		described.Activities = append([]api.CommandActivity(nil), program.Activities...)
		for i := range described.Activities {
			action := actioncard.ForActivity(described, described.Activities[i], result, marks)
			described.Activities[i].Action = &action
		}
		result.CommandProgram = &described
	} else {
		result.Action = actioncard.ForResult(result, marks)
	}
	msg.ToolResult = &result
	return msg
}

func fieldMarks(meta *api.HostSecretRedactionMeta, field string) []api.RedactedSpan {
	if meta == nil {
		return nil
	}
	var out []api.RedactedSpan
	for _, span := range meta.Spans {
		if span.Field == field {
			out = append(out, span)
		}
	}
	return out
}

// TranscriptMessages leaves the recorded messages untouched.
func TranscriptMessages(msgs []api.Message) []api.Message {
	out := make([]api.Message, len(msgs))
	for i, msg := range msgs {
		out[i] = TranscriptMessage(msg)
	}
	return out
}
