package inputs

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/workflow/publication"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/workflow/runstate"
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

// PersistCoordinatorAskAnswer writes the answer to its tool row.
func (m *Cards) PersistCoordinatorAskAnswer(ctx context.Context, sessionID string, ask runstate.CoordinatorAsk) {
	if ask.ToolCallID == "" || ask.State != runstate.CoordinatorAskAnswered {
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
	m.PersistAskUserAnswerForToolCall(ctx, sessionID, ask.ToolCallID, body)
}

func (m *Cards) PersistAskUserAnswerForToolCall(ctx context.Context, sessionID, toolCallID string, body AskUserAnswerBody) {
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
		m.Transcript.PublishPatch(ctx, sessionID, updated)
		return
	}
}

func answeredAskOffMenu(ask runstate.CoordinatorAsk) bool {
	if len(ask.Options) == 0 {
		return false
	}
	for _, choice := range ask.Choices {
		if !runstate.DecisionOptionAllowed(ask.Options, choice) {
			return true
		}
	}
	return false
}

func (m *Cards) AppendAnnouncement(ctx context.Context, sessionID string, msg *api.Message) {
	if m == nil || m.Sessions == nil || msg == nil {
		return
	}
	if err := m.Transcript.Append(ctx, sessionID, *msg); err != nil {
		// The stable message id turns the immediate retry into one logical append.
		_ = m.Transcript.Append(ctx, sessionID, *msg)
	}
}
func (m *Cards) StampFeedbackAnswer(ctx context.Context, sessionID, runID, phaseID, answer, answererID string) {
	if m == nil || m.Sessions == nil || strings.TrimSpace(answer) == "" {
		return
	}
	msgs, err := m.Sessions.GetMessages(ctx, sessionID)
	if err != nil {
		return
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		meta := msgs[i].WorkflowFeedback
		if meta == nil || meta.PhaseID != phaseID || msgs[i].WorkflowRunID != runID {
			continue
		}
		if strings.TrimSpace(meta.Answer) != "" {
			return
		}
		updated := msgs[i]
		metaCopy := *meta
		metaCopy.Answer = strings.TrimSpace(answer)
		if id := strings.TrimSpace(answererID); id != "" {
			metaCopy.ResolvedBy = "user"
			metaCopy.ResolvedByPersonID = id
		}
		updated.WorkflowFeedback = &metaCopy
		if _, err := m.Sessions.UpdateMessage(ctx, sessionID, updated.ID, updated); err != nil {
			return
		}
		m.Transcript.PublishPatch(ctx, sessionID, updated)
		return
	}
}

type Cards struct {
	Sessions   Sessions
	Transcript *publication.Messages
}
