package transcript

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/pkg/api"
)

const maxCommandProjectionBytes = 256 << 10

// projectCommandResult separates host result text from its transport metadata.
func projectCommandResult(message api.Message, parts []api.MessageContentPart) []api.MessageContentPart {
	if message.Role != api.MessageRoleTool || message.Origin != api.MessageOriginTool || message.ToolResult == nil || len(parts) != 1 {
		return parts
	}
	part := parts[0]
	result := message.ToolResult
	if part.Origin != api.MessageOriginTool || part.Authority != api.ContentAuthorityNone || result.Tool != "command" || len(part.Content) > maxCommandProjectionBytes {
		return parts
	}
	if !commandReceiptBound(result) {
		return parts
	}
	receipt := result.CommandProgram
	handles, body := resultHandles(message, part.Content)
	prefix, body, suffix, ok := hostmarker.SplitToolJSONBody(body)
	if !ok || prefix != "" {
		return parts
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &metadata) != nil {
		return parts
	}
	var state struct {
		ProgramID string `json:"program_id"`
		State     string `json:"state"`
		Status    *int   `json:"status"`
		Stdout    string `json:"stdout"`
		Stderr    string `json:"stderr"`
		Refusal   *struct {
			Code     string `json:"code"`
			Feedback string `json:"feedback"`
		} `json:"refusal"`
	}
	if json.Unmarshal([]byte(body), &state) != nil || state.ProgramID != receipt.ID || state.State != receipt.State || state.Status == nil {
		return parts
	}
	if receipt.StatusCode != nil && *receipt.StatusCode != *state.Status {
		return parts
	}
	switch state.State {
	case "running", "completed", "rejected", "failed", "interrupted":
	default:
		return parts
	}
	feedback := ""
	if state.Refusal != nil {
		if state.Refusal.Code == "" || (receipt.StopCode != "" && state.Refusal.Code != receipt.StopCode) {
			return parts
		}
		if receipt.StopCode == "" && receipt.State != "running" {
			return parts
		}
		feedback = state.Refusal.Feedback
		var refusal map[string]json.RawMessage
		if json.Unmarshal(metadata["refusal"], &refusal) != nil {
			return parts
		}
		delete(refusal, "feedback")
		if details, present := refusal["details"]; present && string(details) != "null" {
			if message.ID == "" {
				return parts
			}
			delete(refusal, "details")
			refusal["details_retained"] = json.RawMessage("true")
			refusal["result_message_id"], _ = json.Marshal(message.ID)
		}
		metadata["refusal"], _ = json.Marshal(refusal)
	}
	delete(metadata, "stdout")
	delete(metadata, "stderr")
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return parts
	}
	out := make([]api.MessageContentPart, 0, 5)
	appendText := func(source, label, text string) {
		if text == "" {
			return
		}
		next := part
		next.Source = source
		next.Content = label + text
		out = append(out, next)
	}
	appendText("command_refusal", "Command refusal:\n", feedback)
	appendText("command_metadata", "Command result metadata:\n", strings.Join(append(handles, string(encoded)), "\n"))
	appendStream := func(source, label, text string) {
		if text == "" {
			return
		}
		fence := sourceFence(text)
		appendText(source, label+"\n"+fence+"\n", text+"\n"+fence)
	}
	appendStream("command_stderr", "Command stderr:", state.Stderr)
	appendStream("command_stdout", "Command stdout:", state.Stdout)
	appendText("result_suffix", "", suffix)
	return out
}
