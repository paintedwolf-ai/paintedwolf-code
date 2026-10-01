package messageview

import "github.com/lycaon/lycaon/pkg/api"

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
	return projectContent(out)
}

// TranscriptMessages leaves the recorded messages untouched.
func TranscriptMessages(msgs []api.Message) []api.Message {
	out := make([]api.Message, len(msgs))
	for i, msg := range msgs {
		out[i] = TranscriptMessage(msg)
	}
	return out
}
