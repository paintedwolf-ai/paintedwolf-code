package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const agentEnabledSignal = "session.mode.agent_enabled"

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

func (a RoundAuthority) sessionView(sess *api.Session) *api.Session {
	if sess.Mode == a.Mode {
		return sess
	}
	updated := *sess
	updated.Mode = a.Mode
	return &updated
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
		SignalID: agentEnabledSignal,
		Content:  content,
	}, "", state)
}
