package turnnudges

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

// SpendSoftStop renders the tool-capable landing instruction used only on
// the first coordinator iteration after a running task crosses its ceiling.
// Without the rendered instruction no wind-down round is granted, so a missing
// Reader or failed render yields no nudge.
func (m *Service) SpendSoftStop(ctx context.Context, sess *api.Session, reader prompts.Reader) promptloop.HostNudge {
	if m == nil || sess == nil || sess.IsWorkerChild() {
		return promptloop.HostNudge{}
	}
	stem := anchor.InformRenderFor(ctx, anchor.TurnSpendSoftStop, anchor.MatchContext{Surface: "coordinator", SessionID: sess.ID})
	if stem == "" {
		return promptloop.HostNudge{}
	}
	if !reader.Valid() {
		slog.WarnContext(ctx, "spend soft stop has no request reader; no wind-down round", "session_id", sess.ID)
		return promptloop.HostNudge{}
	}
	content, err := m.renderKick(ctx, reader, stem, nil)
	if err != nil || strings.TrimSpace(content) == "" {
		slog.WarnContext(ctx, "spend soft stop render; no wind-down round", "session_id", sess.ID, "error", err)
		return promptloop.HostNudge{}
	}
	return promptloop.HostNudge{Content: content, SignalID: string(anchor.TurnSpendSoftStop)}
}

// SpendRunway returns one warning per session and ceiling.
func (m *Service) SpendRunway(ctx context.Context, sess *api.Session, reader prompts.Reader, ceilingUSD float64) promptloop.HostNudge {
	if m == nil || sess == nil || !m.Spend.ClaimWarning(ctx, sess, ceilingUSD) {
		return promptloop.HostNudge{}
	}
	return m.RenderWithReader(ctx, sess, reader, anchor.TurnSpendRunwayLow, nil)
}
