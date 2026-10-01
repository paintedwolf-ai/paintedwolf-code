package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// AskUserAnswerBody is the resolved ask_user result.
type AskUserAnswerBody struct {
	Status   string   `json:"status"`
	PhaseID  string   `json:"phase_id"`
	Response string   `json:"response"`
	Choices  []string `json:"choices,omitempty"`
	// OffMenu marks a choice outside the offered options.
	OffMenu      bool   `json:"off_menu,omitempty"`
	ResponseType string `json:"response_type,omitempty"`
	Purpose      string `json:"purpose,omitempty"`
	ResolvedBy   string `json:"resolved_by"`
	AnsweredAt   string `json:"answered_at,omitempty"`
}

// persistCoordinatorAskAnswer writes the answer to its tool row.
func (m *RunManager) persistCoordinatorAskAnswer(ctx context.Context, sessionID string, ask coordinatorAsk) {
	if ask.ToolCallID == "" || ask.State != coordinatorAskAnswered {
		return
	}
	body := AskUserAnswerBody{
		Status: "answered", PhaseID: ask.ID, Response: ask.Response,
		Choices: append([]string(nil), ask.Choices...), ResponseType: string(ask.ResponseType),
		Purpose: ask.Purpose, ResolvedBy: ask.ResolvedBy,
	}
	if ask.AnsweredAt != nil {
		body.AnsweredAt = ask.AnsweredAt.UTC().Format(time.RFC3339Nano)
	}
	body.OffMenu = answeredAskOffMenu(ask)
	m.persistAskUserAnswerForToolCall(ctx, sessionID, ask.ToolCallID, body)
}

func (m *RunManager) persistAskUserAnswerForToolCall(ctx context.Context, sessionID, toolCallID string, body AskUserAnswerBody) {
	if m == nil || m.Sessions == nil {
		return
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return
	}
	content := string(raw)
	msgs, err := m.Sessions.GetMessages(ctx, sessionID)
	if err != nil {
		return
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Role != api.MessageRoleTool || msg.ToolResult == nil {
			continue
		}
		if strings.TrimSpace(msg.ToolResult.ToolCallID) != toolCallID {
			continue
		}
		if tool := strings.TrimSpace(msg.ToolResult.Tool); tool != "" && tool != "ask_user" {
			continue
		}
		updated := msg
		updated.Content = content
		tr := *msg.ToolResult
		tr.Content = content
		tr.Tool = "ask_user"
		updated.ToolResult = &tr
		if _, err := m.Sessions.UpdateMessage(ctx, sessionID, updated.ID, updated); err != nil {
			return
		}
		m.publishMessagePatch(ctx, sessionID, updated)
		return
	}
}

func answeredAskOffMenu(ask coordinatorAsk) bool {
	if len(ask.Options) == 0 {
		return false
	}
	for _, choice := range ask.Choices {
		if !decisionOptionAllowed(ask.Options, choice) {
			return true
		}
	}
	return false
}
