package closeoutassembly

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/pkg/api"
)

type executionObservations struct {
	ModelChecklist string                                `json:"model_authored_checklist,omitempty"`
	SourceChecks   *verification.SourceCheckObservations `json:"source_checks"`
}

type SourceVerification interface {
	SourceObservations(context.Context, *api.Session, []api.Message) *verification.SourceCheckObservations
}

func (m *Service) SetSourceVerification(source SourceVerification) { m.sourceVerification = source }

func (m *Service) executionObservations(ctx context.Context, sessionID string) string {
	if m.store == nil {
		return ""
	}
	messages, err := m.store.GetMessages(ctx, sessionID)
	if err != nil {
		return ""
	}
	var checks *verification.SourceCheckObservations
	if m.sourceVerification != nil {
		sess, err := m.store.Get(ctx, sessionID)
		if err == nil && sess != nil {
			checks = m.sourceVerification.SourceObservations(ctx, sess, messages)
		}
	}
	return executionObservationJSON(messages, checks)
}

// Checklist text remains model-authored; it cannot establish source freshness.
func executionObservationJSON(messages []api.Message, checks *verification.SourceCheckObservations) string {
	if checks == nil {
		return ""
	}
	out := executionObservations{SourceChecks: checks}
	for _, message := range messages[api.UserIntentBoundary(messages):] {
		result := message.ToolResult
		if result == nil || result.Tool != "update_progress" || string(result.Outcome) != "completed" {
			continue
		}
		content, _ := result.ToolArgs["content"].(string)
		out.ModelChecklist = runeclamp.ClampBytes(strings.TrimSpace(content), 800)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(raw)
}
