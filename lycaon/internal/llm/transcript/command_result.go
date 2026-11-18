package transcript

import (
	"encoding/json"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
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
	var state commandResultPayload
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
	feedback, valid := state.projectRefusal(message.ID, receipt)
	if !valid {
		return parts
	}
	encoded, err := surveyjson.Marshal(state.commandResultMetadata)
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
	appendStream("command_stderr", "Command stderr:", ansi.Strip(state.Stderr))
	appendStream("command_stdout", "Command stdout:", ansi.Strip(state.Stdout))
	appendText("result_suffix", "", suffix)
	return out
}

type commandResultPayload struct {
	commandResultMetadata
	ProgramID string `json:"program_id"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Refusal   *struct {
		Code     string          `json:"code"`
		Feedback string          `json:"feedback"`
		Details  json.RawMessage `json:"details"`
	} `json:"refusal"`
}

type commandResultMetadata struct {
	State                    string                  `json:"state"`
	Status                   *int                    `json:"status"`
	Outputs                  []commandStreamMetadata `json:"outputs,omitempty"`
	NonzeroExecutions        []commandExitMetadata   `json:"nonzero_executions,omitempty"`
	OmittedNonzeroExecutions int                     `json:"omitted_nonzero_executions,omitempty"`
	CompletedEffects         int                     `json:"completed_effects,omitempty"`
	UncertainEffects         int                     `json:"uncertain_effects,omitempty"`
	CPUTimeUnmeasured        bool                    `json:"cpu_time_unmeasured,omitempty"`
	Refusal                  *commandRefusalMetadata `json:"refusal,omitempty"`
}

type commandStreamMetadata struct {
	Stream       string                         `json:"stream"`
	TotalBytes   int64                          `json:"total_bytes"`
	TotalLines   int64                          `json:"total_lines"`
	Complete     bool                           `json:"complete"`
	Truncated    bool                           `json:"truncated"`
	OmittedLines int64                          `json:"omitted_lines,omitempty"`
	OutputPath   string                         `json:"output_path"`
	Limit        *api.CommandProgramOutputLimit `json:"limit,omitempty"`
}

type commandExitMetadata struct {
	StatusCode *int `json:"status_code"`
	Span       struct {
		Line int `json:"line"`
	} `json:"span"`
}

type commandRefusalMetadata struct {
	Code            string `json:"code"`
	DetailsRetained bool   `json:"details_retained,omitempty"`
	ResultMessageID string `json:"result_message_id,omitempty"`
}

func (p *commandResultPayload) projectRefusal(messageID string, receipt *api.CommandProgramReceipt) (string, bool) {
	refusal := p.Refusal
	if refusal == nil {
		return "", true
	}
	if refusal.Code == "" || (receipt.StopCode != "" && refusal.Code != receipt.StopCode) ||
		(receipt.StopCode == "" && receipt.State != "running") {
		return "", false
	}
	metadata := &commandRefusalMetadata{Code: refusal.Code}
	if len(refusal.Details) > 0 && string(refusal.Details) != "null" {
		if messageID == "" {
			return "", false
		}
		metadata.DetailsRetained = true
		metadata.ResultMessageID = messageID
	}
	p.commandResultMetadata.Refusal = metadata
	return refusal.Feedback, true
}
