package instructions

import (
	"context"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) BootstrapProgress(ctx context.Context, sessionID string, msg api.Message) {
	if m == nil || m.progress == nil || !promptinput.VisibleIntent(msg) {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil || sess.ParentSessionID != "" {
		return
	}
	runID := m.ActiveWorkflowRunID(ctx, sessionID)
	if runID == "" {
		return
	}
	if refreshed := progress.EnsureBootstrap(ctx, m.progress, sessionID, runID, msg.Content); refreshed {
		progress.NotifyWriteObservers(ctx, progress.WriteEvent{SessionID: sessionID})
	}
}

func (m *Service) ActiveWorkflowRunID(ctx context.Context, sessionID string) string {
	if m == nil || m.workflows == nil {
		return ""
	}
	run, err := m.workflows.GetActive(ctx, sessionID)
	if err != nil || run == nil {
		return ""
	}
	return run.ID
}
