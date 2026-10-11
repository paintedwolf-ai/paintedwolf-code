package turnnudges

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/pkg/api"
)

// SpendSoftStop renders the tool-capable landing instruction used only on
// the first coordinator iteration after a running task crosses its ceiling.
func (m *Service) SpendSoftStop(ctx context.Context, sess *api.Session) promptloop.HostNudge {
	if m == nil || sess == nil || sess.IsWorkerChild() {
		return promptloop.HostNudge{}
	}
	return m.Render(ctx, sess, anchor.TurnSpendSoftStop, nil)
}

// SpendRunway returns one warning per session and ceiling.
func (m *Service) SpendRunway(ctx context.Context, sess *api.Session, ceilingUSD float64) promptloop.HostNudge {
	if m == nil || !m.Spend.ClaimWarning(ctx, sess, ceilingUSD) {
		return promptloop.HostNudge{}
	}
	return m.Render(ctx, sess, anchor.TurnSpendRunwayLow, nil)
}
