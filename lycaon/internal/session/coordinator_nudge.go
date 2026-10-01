package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

// GuidanceNudge carries structured feedback and optional policy copy.
type GuidanceNudge struct {
	Code    string
	Data    map[string]any
	Copy    map[string]string
	Subject *api.FeedbackSubject
}

// queueCoordinatorGuidanceNudge queues one dynamic rule code from the hint registry.
func (m *Manager) queueCoordinatorGuidanceNudge(ctx context.Context, sessionID, code string, data map[string]any, subject *api.FeedbackSubject) {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	code = strings.TrimSpace(code)
	formatted, err := guidance.FormatCoordinatorNudge(ctx, m.rejectFmt, code, data)
	if err != nil || strings.TrimSpace(formatted) == "" {
		return
	}
	if subject == nil {
		subject = guidanceNudgeSubject(sessionID, data)
	}
	m.ensureCoordinatorRuntime().Kicks().QueuePendingGuidance(sessionID, formatted, api.ToolFeedback{
		Code: code, Details: data, Subject: subject,
	})
}

func guidanceNudgeSubject(sessionID string, data map[string]any) *api.FeedbackSubject {
	if raw, ok := data["subject"].(map[string]any); ok {
		kind, _ := raw["kind"].(string)
		id, _ := raw["id"].(string)
		if strings.TrimSpace(kind) != "" && strings.TrimSpace(id) != "" {
			return &api.FeedbackSubject{Kind: strings.TrimSpace(kind), ID: strings.TrimSpace(id)}
		}
	}
	if sessionID == "" {
		return nil
	}
	return &api.FeedbackSubject{Kind: "session", ID: sessionID}
}

// queueCoordinatorGuidanceAdvisories queues each advisory in evaluation order.
func (m *Manager) queueCoordinatorGuidanceAdvisories(ctx context.Context, sessionID string, items []GuidanceNudge) {
	for _, item := range items {
		if item.Copy == nil {
			m.queueCoordinatorGuidanceNudge(ctx, sessionID, item.Code, item.Data, item.Subject)
			continue
		}
		text, err := guidance.RenderPolicyCopy(ctx, item.Code, string(oar.EffectNudge), item.Copy)
		if err != nil || strings.TrimSpace(text) == "" || m == nil || strings.TrimSpace(sessionID) == "" {
			continue
		}
		subject := item.Subject
		if subject == nil {
			subject = guidanceNudgeSubject(sessionID, item.Data)
		}
		m.ensureCoordinatorRuntime().Kicks().QueuePendingGuidance(sessionID, text, api.ToolFeedback{Code: item.Code, Details: item.Data, Subject: subject})
	}
}

// QueueGuidanceNudge enqueues a structured guidance nudge for the session.
func (m *Manager) QueueGuidanceNudge(ctx context.Context, sessionID, code string, data map[string]any) {
	m.queueCoordinatorGuidanceNudge(ctx, sessionID, code, data, nil)
}
