package runtime

import (
	"context"

	"github.com/lycaon/lycaon/internal/hostctx"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *SessionPolicy) activeRunForPolicy(ctx context.Context, id string) (*api.WorkflowRun, error) {
	run, err := m.Runs.ActiveBySession(ctx, id)
	if err != nil || !runstate.IsAmbientRun(run) {
		return run, err
	}
	mode, err := m.sessionMode(ctx, id)
	if err != nil || mode == api.SessionModeAsk {
		return nil, err
	}
	return run, nil
}

func (m *SessionPolicy) sessionMode(ctx context.Context, id string) (api.SessionMode, error) {
	if mode, ok := hostctx.RoundMode(ctx, id); ok {
		return mode, nil
	}
	if m.ResolveEffectiveMode == nil {
		return "", store.ErrSessionModeUnavailable
	}
	mode, err := m.ResolveEffectiveMode(ctx, id)
	if err != nil {
		return "", err
	}
	return mode.WithModeIntent(nil)
}
