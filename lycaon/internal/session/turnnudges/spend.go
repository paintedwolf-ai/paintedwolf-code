package turnnudges

import (
	"context"
	"strings"

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
	stem := anchor.InformRenderFor(ctx, anchor.TurnSpendSoftStop, anchor.MatchContext{Surface: "coordinator", SessionID: sess.ID})
	if stem == "" {
		return promptloop.HostNudge{}
	}
	content, err := m.renderKick(ctx, m.activeTurnReader(ctx, sess, ""), stem, nil)
	if err != nil || strings.TrimSpace(content) == "" {
		return promptloop.HostNudge{}
	}
	return promptloop.HostNudge{Content: content, SignalID: string(anchor.TurnSpendSoftStop)}
}

// SpendRunway returns one warning per session and ceiling.
func (m *Service) SpendRunway(ctx context.Context, sess *api.Session, ceilingUSD float64) promptloop.HostNudge {
	if m == nil || sess == nil || !m.Spend.ClaimWarning(ctx, sess, ceilingUSD) {
		return promptloop.HostNudge{}
	}
	return m.RenderWithReader(ctx, sess, m.activeTurnReader(ctx, sess, ""), anchor.TurnSpendRunwayLow, nil)
}
