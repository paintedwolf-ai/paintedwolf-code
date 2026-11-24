package promptloop

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// supersedeHostNudges retires rows that nudge replaces so current instructions
// follow the latest evidence. A repair replaces every active repair; stored rows
// stay for audit.
func (l *turnNudges) supersedeHostNudges(
	ctx context.Context,
	sessionID string,
	history []api.Message,
	nudge api.Message,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	signalID := strings.TrimSpace(nudge.HostSignalID)
	repair := api.IsActiveRepair(nudge)
	out := make([]api.Message, 0, len(history)+1)
	for _, msg := range history {
		superseded := msg.Origin == api.MessageOriginHost && msg.DraftStatus != api.DraftStatusWithdrawn &&
			((signalID != "" && strings.TrimSpace(msg.HostSignalID) == signalID) || (repair && api.IsActiveRepair(msg)))
		if !superseded {
			out = append(out, msg)
			continue
		}
		if dropDeferredUserNudge(st, msg.ID) {
			continue
		}
		if err := l.withdrawHostRow(ctx, sessionID, &msg); err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, nil
}

// expireSettledRepairs withdraws repair guidance left by settled turns, so a new
// user request never inherits instructions aimed at an earlier attempt.
func (l *turnNudges) expireSettledRepairs(ctx context.Context, sessionID string, history []api.Message) ([]api.Message, error) {
	boundary := api.UserIntentBoundary(history)
	for i := 0; i < boundary; i++ {
		if !api.IsActiveRepair(history[i]) {
			continue
		}
		if err := l.withdrawHostRow(ctx, sessionID, &history[i]); err != nil {
			return nil, err
		}
	}
	return history, nil
}

func (l *turnNudges) withdrawHostRow(ctx context.Context, sessionID string, msg *api.Message) error {
	msg.DraftStatus = api.DraftStatusWithdrawn
	if l == nil || l.Projection.Deps.UpdateMessage == nil || msg.ID == "" {
		return nil
	}
	return l.Projection.Deps.UpdateMessage(ctx, sessionID, msg.ID, *msg)
}

func dropDeferredUserNudge(st *promptLoopTurnState, id string) bool {
	if st == nil || id == "" {
		return false
	}
	for i := range st.deferredUserNudges {
		if st.deferredUserNudges[i].ID == id {
			st.deferredUserNudges = append(st.deferredUserNudges[:i], st.deferredUserNudges[i+1:]...)
			return true
		}
	}
	return false
}
