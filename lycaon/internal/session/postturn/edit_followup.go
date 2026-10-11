package postturn

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/pkg/api"
)

// EditFollowUp detects two consecutive turns answered with inline edits.
func (m *Service) EditFollowUp(ctx context.Context, sess *api.Session, userPrompt string) {
	if m == nil || sess == nil || strings.TrimSpace(userPrompt) == "" {
		return
	}
	if !surface.IsCoordinatorSession(sess) {
		return
	}
	history, err := m.store.GetMessages(ctx, sess.ID)
	if err != nil {
		return
	}
	if EditFollowUpRepeat(history) {
		m.guidance.Emit(ctx, sess.ID, anchor.EditFollowUpRepeat, anchor.Envelope{})
	}
}

// EditFollowUpRepeat uses successful file-edit receipts between visible user turns.
func EditFollowUpRepeat(history []api.Message) bool {
	segmentsWithEdits := 0
	segmentHasEdit := false
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg.Role == api.MessageRoleUser && msg.Visibility != api.MessageVisibilityInternal {
			if !segmentHasEdit {
				return false
			}
			segmentsWithEdits++
			if segmentsWithEdits == 2 {
				return true
			}
			segmentHasEdit = false
			continue
		}
		if msg.ToolResult != nil && msg.ToolResult.FileEdit != nil {
			segmentHasEdit = true
		}
	}
	return false
}
