package transcript

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) DeliveredWorkflowPhase(ctx context.Context, sessionID, runID, phase string) (bool, error) {
	if m == nil || m.store == nil || strings.TrimSpace(runID) == "" {
		return false, nil
	}
	messages, err := m.store.GetMessages(ctx, sessionID)
	if err != nil {
		return false, err
	}
	since := api.UserIntentBoundary(messages)
	for i := len(messages) - 1; i >= 0 && i >= since; i-- {
		msg := messages[i]
		if msg.Role == api.MessageRoleAssistant && msg.Kind == api.MessageKindCompletionReport && len(msg.ToolCalls) == 0 &&
			msg.Visibility == api.MessageVisibilityTranscript && msg.WorkflowRunID == runID &&
			msg.CompletionReport != nil && msg.CompletionReport.Scope == api.CompletionReportScopePhase &&
			msg.CompletionReport.Phase == phase {
			return true, nil
		}
	}
	return false, nil
}
