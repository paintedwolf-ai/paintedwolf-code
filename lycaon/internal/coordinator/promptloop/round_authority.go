package promptloop

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// RoundAuthority is the root ceiling frozen with the model's tool offer.
type RoundAuthority struct {
	RootSessionID        string
	Mode                 api.SessionMode
	ReadOnly             bool
	UpgradedFromAsk      bool
	AgentNoticeDelivered bool
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
		RootSessionID:        root.RootSessionID,
		Mode:                 root.Mode,
		ReadOnly:             workerReadOnly || root.Mode == api.SessionModeAsk,
		UpgradedFromAsk:      root.UpgradedFromAsk,
		AgentNoticeDelivered: root.AgentNoticeDelivered,
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

func (l *PromptLoop) appendAgentEnabledNudge(ctx context.Context, sess *api.Session, sessionID string, state *promptLoopTurnState) ([]api.Message, error) {
	if l.Closeout.Deps.RenderHostKick == nil {
		return state.history, nil
	}
	var hasRunningJobs, hasActiveWorkers bool
	if l.Context != nil && l.Context.Deps.LiveResources != nil {
		res := l.Context.Deps.LiveResources(sessionID)
		hasRunningJobs = res.CommandJobs || res.HeldCalls
	}
	if l.Context != nil && l.Context.Deps.ImplementSessionState != nil && sess != nil {
		impl := l.Context.Deps.ImplementSessionState(ctx, sess)
		hasActiveWorkers = impl.WorkersInFlight > 0 || impl.WorkersDelivered > 0
	}
	content, err := l.Closeout.Deps.RenderHostKick(ctx, "session-agent-enabled", map[string]any{
		"previous_mode":      "Ask",
		"current_mode":       "Agent",
		"has_running_jobs":   hasRunningJobs,
		"has_active_workers": hasActiveWorkers,
	})
	if err != nil {
		return nil, err
	}
	nudge := newHostNudge(HostNudge{
		SignalID: anchor.SessionAgentEnabled.String(),
		Content:  content,
	}, "")
	nudge.ID = store.AgentEnabledMessageID(sessionID)
	if l.Projection == nil || l.Projection.Deps.AppendMessages == nil {
		if state != nil {
			state.agentEnabledNoticeDelivered = true
		}
		return append(state.history, nudge), nil
	}
	stamped, err := l.Projection.appendMessage(ctx, sessionID, nudge)
	if err != nil {
		if errors.Is(err, store.ErrDuplicateMessageID) {
			if state != nil {
				state.agentEnabledNoticeDelivered = true
			}
			return state.history, nil
		}
		return nil, err
	}
	if state != nil {
		state.agentEnabledNoticeDelivered = true
	}
	return append(state.history, stamped), nil
}

func shouldDeliverAgentEnabledNudge(sess *api.Session, previous api.SessionMode, authority RoundAuthority, state *promptLoopTurnState) bool {
	if sess == nil || sess.IsWorkerChild() || authority.Mode != api.SessionModeAgent {
		return false
	}
	if authority.AgentNoticeDelivered {
		return false
	}
	if state != nil && state.agentEnabledNoticeDelivered {
		return false
	}
	if !authority.UpgradedFromAsk && previous != api.SessionModeAsk {
		return false
	}
	if state != nil {
		signal := anchor.SessionAgentEnabled.String()
		deterministicID := store.AgentEnabledMessageID(sess.ID)
		for _, msg := range state.history {
			if msg.HostSignalID == signal || msg.ID == deterministicID {
				state.agentEnabledNoticeDelivered = true
				return false
			}
		}
	}
	return true
}
