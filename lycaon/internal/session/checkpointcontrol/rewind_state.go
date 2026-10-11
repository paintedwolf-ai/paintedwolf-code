package checkpointcontrol

import (
	"context"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// rollbackEphemeralState reconciles domain state with the truncated timeline.
func (m *Rewinds) rollbackEphemeralState(ctx context.Context, sess *api.Session) {
	if m == nil || sess == nil {
		return
	}
	sessionID := strings.TrimSpace(sess.ID)
	rootID := sessiontree.RootID(ctx, m.store, sessionID)
	m.captures.Capture.Reset(rootID)
	m.runtime.ResetWorkers(ctx, sess, rootID)
	m.runtime.ResetCoordinator(ctx, sessionID)
	m.runtime.ResetTurnLedgers(sessionID, rootID)
	m.runtime.ResetProgress(rootID)
	m.runtime.ResetQueue(ctx, sessionID)
}
