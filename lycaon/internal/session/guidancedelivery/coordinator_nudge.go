package guidancedelivery

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/policyfeedback"
	"github.com/lycaon/lycaon/pkg/api"
)

// Queue queues one dynamic rule code from the hint registry.
func (m *Service) Queue(ctx context.Context, sessionID, code string, data map[string]any, subject *api.FeedbackSubject) {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	code = strings.TrimSpace(code)
	formatted, err := guidance.FormatCoordinatorNudge(ctx, m.rejectFmt, code, data)
	if err != nil || strings.TrimSpace(formatted) == "" {
		return
	}
	if subject == nil {
		subject = policyfeedback.Subject(sessionID, data)
	}
	m.kicks.QueuePendingGuidance(sessionID, formatted, api.ToolFeedback{
		Code: code, Details: data, Subject: subject,
	})
}

// QueueAdvisories queues each advisory in evaluation order.
func (m *Service) QueueAdvisories(ctx context.Context, sessionID string, items []guidance.GuidanceNudge) {
	for _, item := range items {
		if item.Copy == nil {
			m.Queue(ctx, sessionID, item.Code, item.Data, item.Subject)
			continue
		}
		text, err := guidance.RenderPolicyCopy(ctx, item.Code, string(oar.EffectNudge), item.Copy)
		if err != nil || strings.TrimSpace(text) == "" || m == nil || strings.TrimSpace(sessionID) == "" {
			continue
		}
		subject := item.Subject
		if subject == nil {
			subject = policyfeedback.Subject(sessionID, item.Data)
		}
		m.kicks.QueuePendingGuidance(sessionID, text, api.ToolFeedback{Code: item.Code, Details: item.Data, Subject: subject})
	}
}

// QueueNudge enqueues a structured guidance nudge for the session.
func (m *Service) QueueNudge(ctx context.Context, sessionID, code string, data map[string]any) {
	m.Queue(ctx, sessionID, code, data, nil)
}
