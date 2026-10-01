package session

import (
	"context"
	"log/slog"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/pkg/api"
)

// SpendRunwayFraction is the share of the ceiling at which a session is warned
// once when no valid configured warning ratio is available.
const SpendRunwayFraction = 0.8

// SpendCeilingState is the ceiling posture for one session at one moment,
// computed from a single cost rollup.
type SpendCeilingState struct {
	// Reached is true when the armed, priced ceiling has been met.
	Reached bool
	// Low is true below the ceiling after spend has met the warning point.
	Low bool
	// CeilingUSD is the effective ceiling; zero when the gate is inactive.
	CeilingUSD float64
	// SpentUSD is the estimated rollup; zero when the gate is inactive.
	SpentUSD float64
	// Coverage is the host classification of SpentUSD; the counters are its causes.
	Coverage            api.CostEstimateCoverage
	UnpricedTokens      int
	UnknownChargedCalls int
	// SoftStop is true when a running coordinator may take one bounded landing round.
	SoftStop bool
}

// reached builds the ceiling error carrying the same coverage facts.
func (st SpendCeilingState) reached() *SessionSpendCeilingReached {
	return &SessionSpendCeilingReached{
		CeilingUSD:          st.CeilingUSD,
		SpentUSD:            st.SpentUSD,
		Coverage:            st.Coverage,
		UnpricedTokens:      st.UnpricedTokens,
		UnknownChargedCalls: st.UnknownChargedCalls,
	}
}

// spendCeilingState evaluates one cost summary.
func (m *Manager) spendCeilingState(ctx context.Context, sessionID string, sess *api.Session) (SpendCeilingState, error) {
	if m == nil || m.cost == nil {
		return SpendCeilingState{}, nil
	}
	cfg := m.effectiveLimits(ctx, sess)
	if !cfg.SpendCeilingEnabled || cfg.SessionSpendCeilingUSD <= 0 {
		return SpendCeilingState{}, nil
	}
	summary, err := m.cost.Summary(ctx, api.CostScopeSession, m.spendCeilingScopeID(ctx, sessionID, sess), "")
	if err != nil {
		return SpendCeilingState{}, err
	}
	if !summary.Priced() {
		return SpendCeilingState{}, nil
	}
	spentUSD := float64(summary.EstimatedNanoUsd) / 1e9
	st := SpendCeilingState{
		CeilingUSD:          cfg.SessionSpendCeilingUSD,
		SpentUSD:            spentUSD,
		SoftStop:            cfg.SpendSoftStopEnabled(),
		Coverage:            summary.EstimateCoverage,
		UnpricedTokens:      summary.UnpricedTokens,
		UnknownChargedCalls: summary.UnknownChargedCalls,
	}
	if spentUSD >= cfg.SessionSpendCeilingUSD {
		st.Reached = true
		return st, nil
	}
	warningRatio := cfg.SpendWarningRatio
	if warningRatio <= 0 {
		warningRatio = SpendRunwayFraction
	}
	if spentUSD >= cfg.SessionSpendCeilingUSD*warningRatio {
		st.Low = true
	}
	return st, nil
}

// spendSoftStopNudge renders the tool-capable landing instruction used only on
// the first coordinator iteration after a running task crosses its ceiling.
func (m *Manager) spendSoftStopNudge(ctx context.Context, sess *api.Session) promptloop.HostNudge {
	if m == nil || sess == nil || sess.IsWorkerChild() {
		return promptloop.HostNudge{}
	}
	return m.renderHostNudge(ctx, sess, anchor.TurnSpendSoftStop, nil)
}

// spendRunwayNudge returns one warning per session and ceiling.
func (m *Manager) spendRunwayNudge(ctx context.Context, sess *api.Session, ceilingUSD float64) promptloop.HostNudge {
	if m == nil || m.cost == nil || sess == nil || sess.IsWorkerChild() || ceilingUSD <= 0 {
		return promptloop.HostNudge{}
	}
	scopeID := m.spendCeilingScopeID(ctx, sess.ID, sess)
	claimed, err := m.cost.ClaimSpendWarning(ctx, scopeID, ceilingUSD)
	if err != nil {
		slog.WarnContext(ctx, "claim spend warning", "session_id", scopeID, "error", err)
		return promptloop.HostNudge{}
	}
	if !claimed {
		return promptloop.HostNudge{}
	}
	return m.renderHostNudge(ctx, sess, anchor.TurnSpendRunwayLow, nil)
}
