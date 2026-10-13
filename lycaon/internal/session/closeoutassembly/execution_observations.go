package closeoutassembly

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/pkg/api"
)

type executionObservation struct {
	InvocationID     string               `json:"invocation_id"`
	Tool             string               `json:"tool"`
	RequestedCommand string               `json:"requested_command,omitempty"`
	Invoked          bool                 `json:"invoked"`
	Status           api.InvocationStatus `json:"status"`
	SourceVerdict    string               `json:"source_verdict,omitempty"`
	SourceRevision   string               `json:"source_revision,omitempty"`
}

type executionObservations struct {
	ModelChecklist string                 `json:"model_authored_checklist"`
	Actions        []executionObservation `json:"recorded_actions"`
	Omitted        int                    `json:"older_actions_omitted"`
}

func (m *Service) executionObservations(ctx context.Context, sessionID string) string {
	if m.store == nil {
		return ""
	}
	messages, err := m.store.GetMessages(ctx, sessionID)
	if err != nil {
		return ""
	}
	return executionObservationJSON(messages)
}

// This is an action inventory, never a semantic verdict on checklist items.
func executionObservationJSON(messages []api.Message) string {
	out := executionObservations{Actions: []executionObservation{}}
	const maxActions = 6
	for _, message := range messages[api.UserIntentBoundary(messages):] {
		result := message.ToolResult
		if result == nil {
			continue
		}
		if result.Tool == "update_progress" && string(result.Outcome) == "completed" {
			content, _ := result.ToolArgs["content"].(string)
			out.ModelChecklist = runeclamp.ClampBytes(strings.TrimSpace(content), 800)
		}
		receipt := result.Invocation
		if receipt == nil || (receipt.Tool != "command" && receipt.Tool != "verify") {
			continue
		}
		command, _ := result.ToolArgs["command"].(string)
		out.Actions = append(out.Actions, executionObservation{
			InvocationID: receipt.ID, Tool: receipt.Tool, RequestedCommand: runeclamp.ClampBytes(command, 240),
			Invoked: receipt.Invoked, Status: receipt.Status, SourceVerdict: receipt.SourceVerdict,
			SourceRevision: receipt.SourceRevision,
		})
		if len(out.Actions) > maxActions {
			out.Actions = out.Actions[1:]
			out.Omitted++
		}
	}
	if out.ModelChecklist == "" {
		return ""
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(raw)
}
