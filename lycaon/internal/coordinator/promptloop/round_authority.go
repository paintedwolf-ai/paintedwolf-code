package promptloop

import (
	"context"
	"log/slog"
	"slices"

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
		scratchDir := state.machine.Surface.SessionScratchDir
		state.machine = l.Deps.RefreshRoundMachine(ctx, &updated, profileID)
		if scratchDir != "" && state.machine.Surface.SessionScratchDir == "" {
			state.machine.Surface.SessionScratchDir = scratchDir
			state.machine.Surface.Fingerprint = state.machine.Surface.CacheFingerprint()
		}
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

// appendAgentEnabledNudge renders against the next request's projection, which
// the upgraded mode widens.
func (l *PromptLoop) appendAgentEnabledNudge(ctx context.Context, sess *api.Session, sessionID, profileID string, state *promptLoopTurnState) ([]api.Message, error) {
	if l.Closeout.Deps.RenderHostKick == nil {
		return state.history, nil
	}
	turn, err := l.Context.coordinatorToolsForTurn(ctx, sess, profileID, state.history, 0, 0, state.coordinatorFrame, nil)
	if err != nil {
		return nil, err
	}
	reader, err := l.Context.requestReader(ctx, sess, turn, turn.Metas)
	if err != nil {
		slog.WarnContext(ctx, "agent-enabled nudge reader", "session_id", sessionID, "error", err)
		return state.history, nil
	}
	content, err := l.Closeout.Deps.RenderHostKick(ctx, reader, "session-agent-enabled", nil)
	if err != nil {
		return nil, err
	}
	return l.Nudges.appendHostNudge(ctx, sessionID, state.history, HostNudge{
		SignalID: anchor.SessionAgentEnabled.String(),
		Content:  content,
	}, "", state)
}

func (l *promptContext) swapObservedMode(sessionID string, mode api.SessionMode) (api.SessionMode, bool) {
	if sessionID == "" {
		return "", false
	}
	if fn := l.Deps.SwapObservedSessionMode; fn != nil {
		return fn(sessionID, mode)
	}
	return "", false
}

// observeModeUpgrade reports whether this coordinator round is the first round
// observing that sessionID switched from Ask to Agent (mid-turn or between turns).
func (l *promptContext) observeModeUpgrade(
	sess *api.Session,
	sessionID string,
	previous api.SessionMode,
	current api.SessionMode,
	history []api.Message,
) bool {
	if sess == nil || sess.IsWorkerChild() {
		return false
	}
	prevObserved, hadObserved := l.swapObservedMode(sessionID, current)
	if current != api.SessionModeAgent || hasAgentEnabledSignal(history) {
		return false
	}
	if previous == api.SessionModeAsk || (hadObserved && prevObserved == api.SessionModeAsk) {
		return true
	}
	return !hadObserved && historyHadAskRejection(history)
}

func hasAgentEnabledSignal(history []api.Message) bool {
	want := anchor.SessionAgentEnabled.String()
	for _, msg := range history {
		if msg.HostSignalID == want {
			return true
		}
	}
	return false
}

func historyHadAskRejection(history []api.Message) bool {
	for _, msg := range history {
		if msg.ToolResult != nil && slices.Contains(msg.ToolResult.Codes, tools.AskModeActiveCode) {
			return true
		}
	}
	return false
}
