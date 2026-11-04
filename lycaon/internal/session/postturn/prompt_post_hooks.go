package postturn

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/pkg/api"
)

func turnCalledUpdateProgress(turnTools []string) bool {
	for _, tool := range turnTools {
		if strings.TrimSpace(tool) == "update_progress" {
			return true
		}
	}
	return false
}

// After runs post-turn hooks. Workers skip phase-exit, progress, and grounding.
func (m *Service) After(ctx context.Context, sessionID, profileID string, turnTools []string) error {
	isWorker := false
	if m.store != nil {
		if sess, err := m.store.Get(ctx, strings.TrimSpace(sessionID)); err == nil &&
			sess != nil && sess.IsWorkerChild() {
			isWorker = true
		}
	}
	if isWorker {
		return nil
	}
	if m.WorkflowExit(ctx, sessionID, profileID, turnTools) {
		return nil
	}
	m.ProgressMissing(ctx, sessionID, turnTools)
	if m.grounding == nil {
		return nil
	}
	if err := m.grounding.AfterPrompt(ctx, sessionID, turnTools); err != nil {
		var nudge *guidance.ErrGroundingNudge
		if errors.As(err, &nudge) {
			m.guidance.QueueAdvisories(ctx, sessionID, nudge.Nudges())
			return nil
		}
		if errors.Is(err, guidance.ErrGroundingEscalated) {
			return err
		}
	}
	// Feedback on the turn that just ended commits with that turn, so it reads as
	// history and not as a condition on the next user message. A failed take stays
	// staged and arrives with the next turn.
	if m.guidance == nil {
		return nil
	}
	if _, err := m.guidance.TakePolicy(ctx, sessionID); err != nil {
		slog.WarnContext(ctx, "post-turn feedback not committed", "session_id", sessionID, "err", err)
	}
	return nil
}

// ProgressMissing nudges the coordinator to author a progress checklist via
// update_progress when it attempts progress-gated tools without a checklist.
func (m *Service) ProgressMissing(ctx context.Context, sessionID string, turnTools []string) {
	if m == nil || m.progress == nil {
		return
	}
	if m.store != nil {
		if sess, err := m.store.Get(ctx, strings.TrimSpace(sessionID)); err == nil && sess != nil {
			if sess.Posture == api.SessionPostureSpec || sess.IsWorkerChild() {
				return
			}
		}
	}
	if turnCalledUpdateProgress(turnTools) || !progress.TurnHasProgressGatedTool(turnTools) {
		return
	}
	if !progress.ProgressMissing(m.progress.Get(ctx, sessionID)) {
		return
	}
	m.guidance.Emit(ctx, sessionID, anchor.ProgressMissing, anchor.Envelope{Vars: map[string]any{
		"tool": progress.FirstProgressGatedTool(turnTools),
	}})
}
