package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// RoundAuthority is the root ceiling frozen with the model's tool offer.
type RoundAuthority struct {
	RootSessionID string
	Mode          api.SessionMode
	ReadOnly      bool
}

func (l *promptContext) roundAuthority(ctx context.Context, sess *api.Session, workerReadOnly bool) (RoundAuthority, error) {
	if sess == nil || l.Deps.ResolveEffectiveMode == nil {
		return RoundAuthority{}, store.ErrSessionModeUnavailable
	}
	root, err := l.Deps.ResolveEffectiveMode(ctx, sess.ID)
	if err != nil {
		return RoundAuthority{}, err
	}
	if root.RootSessionID == "" || root.Mode != api.SessionModeAgent && root.Mode != api.SessionModeAsk {
		return RoundAuthority{}, store.ErrSessionModeUnavailable
	}
	return RoundAuthority{
		RootSessionID: root.RootSessionID, Mode: root.Mode, ReadOnly: workerReadOnly || root.Mode == api.SessionModeAsk,
	}, nil
}

func (l *promptContext) applyRoundAuthority(ctx context.Context, sess *api.Session, authority RoundAuthority, profileID string, state *promptLoopTurnState) (*api.Session, error) {
	if sess.Mode == authority.Mode {
		return sess, nil
	}
	if sess.Mode == api.SessionModeAsk && authority.Mode == api.SessionModeAgent && !sess.IsWorkerChild() && l.Deps.ActivateAgentWorkflow != nil {
		if err := l.Deps.ActivateAgentWorkflow(ctx, sess.ID); err != nil {
			return nil, err
		}
	}
	updated := *sess
	updated.Mode = authority.Mode
	if l.Deps.RefreshRoundMachine != nil {
		state.machine = l.Deps.RefreshRoundMachine(ctx, &updated, profileID)
	}
	return &updated, nil
}

func (a RoundAuthority) toolContext(base tools.ToolContext) tools.ToolContext {
	if a.RootSessionID != "" {
		base.Identity.RootSessionID = a.RootSessionID
	}
	base.Source.AskMode = a.Mode == api.SessionModeAsk
	base.Source.ReadOnly = a.ReadOnly
	return base
}

func (l *PromptLoop) appendAgentEnabledNudge(ctx context.Context, sessionID string, state *promptLoopTurnState) ([]api.Message, error) {
	if l.Closeout.Deps.RenderHostKick == nil {
		return state.history, nil
	}
	content, err := l.Closeout.Deps.RenderHostKick(ctx, "session-agent-enabled", nil)
	if err != nil {
		return nil, err
	}
	return l.Nudges.appendHostNudge(ctx, sessionID, state.history, HostNudge{
		SignalID: anchor.SessionAgentEnabled.String(),
		Content:  content,
	}, "", state)
}
