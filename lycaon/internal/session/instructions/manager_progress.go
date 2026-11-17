package instructions

import (
	"context"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) BootstrapProgress(ctx context.Context, sessionID string, msg api.Message) error {
	if m == nil || m.progress == nil || !promptinput.VisibleIntent(msg) {
		return nil
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess == nil || sess.ParentSessionID != "" {
		return nil
	}
	runID := m.ActiveWorkflowRunID(ctx, sessionID)
	if runID == "" {
		return nil
	}
	_, err = progress.EnsureBootstrap(ctx, m.progress, sessionID, runID, msg.Content)
	return err
}

func (m *Service) ActiveWorkflowRunID(ctx context.Context, sessionID string) string {
	if m == nil || m.workflows == nil {
		return ""
	}
	run, err := m.workflows.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil {
		return ""
	}
	return run.ID
}
