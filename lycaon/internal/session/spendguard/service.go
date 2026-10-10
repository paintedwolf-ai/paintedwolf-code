package spendguard

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

type Limits interface {
	Effective(context.Context, *api.Session) settings.SessionLimits
}
type Costs interface {
	Summary(context.Context, api.CostScope, string, string) (api.CostSummary, error)
	ClaimSpendWarning(context.Context, string, float64) (bool, error)
	ClearSpendWarning(context.Context, string) error
}
type Service struct {
	store  tree.Reader
	limits Limits
	cost   Costs
}

func New(store tree.Reader, limits Limits, costs Costs) *Service {
	return &Service{store: store, limits: limits, cost: costs}
}
func (s *Service) ClaimWarning(ctx context.Context, sess *api.Session, ceilingUSD float64) bool {
	if s == nil || s.cost == nil || sess == nil || sess.IsWorkerChild() || ceilingUSD <= 0 {
		return false
	}
	scopeID := s.scopeID(ctx, sess.ID, sess)
	claimed, err := s.cost.ClaimSpendWarning(ctx, scopeID, ceilingUSD)
	if err != nil {
		slog.WarnContext(ctx, "claim spend warning", "session_id", scopeID, "error", err)
		return false
	}
	return claimed
}
func (s *Service) Forget(ctx context.Context, sessionID string) {
	if s != nil && s.cost != nil {
		if err := s.cost.ClearSpendWarning(ctx, sessionID); err != nil {
			slog.WarnContext(ctx, "clear spend warning", "session_id", sessionID, "error", err)
		}
	}
}

// DefaultWarningFraction is the share of the ceiling at which a session is warned
// once when no valid configured warning ratio is available.
const DefaultWarningFraction = 0.8

// CeilingState is the ceiling posture for one session at one moment,
// computed from a single cost rollup.
type CeilingState struct {
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
func (st CeilingState) ReachedError() *CeilingReached {
	return &CeilingReached{
		CeilingUSD:          st.CeilingUSD,
		SpentUSD:            st.SpentUSD,
		Coverage:            st.Coverage,
		UnpricedTokens:      st.UnpricedTokens,
		UnknownChargedCalls: st.UnknownChargedCalls,
	}
}

// State evaluates one cost summary.
func (m *Service) State(ctx context.Context, sessionID string, sess *api.Session) (CeilingState, error) {
	if m == nil || m.cost == nil {
		return CeilingState{}, nil
	}
	cfg := m.limits.Effective(ctx, sess)
	if !cfg.SpendCeilingEnabled || cfg.SessionSpendCeilingUSD <= 0 {
		return CeilingState{}, nil
	}
	summary, err := m.cost.Summary(ctx, api.CostScopeSession, m.scopeID(ctx, sessionID, sess), "")
	if err != nil {
		return CeilingState{}, err
	}
	if !summary.Priced() {
		return CeilingState{}, nil
	}
	spentUSD := float64(summary.EstimatedNanoUsd) / 1e9
	st := CeilingState{
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
		warningRatio = DefaultWarningFraction
	}
	if spentUSD >= cfg.SessionSpendCeilingUSD*warningRatio {
		st.Low = true
	}
	return st, nil
}

// Check enforces the root-session spend rollup.
func (m *Service) Check(ctx context.Context, sessionID string, sess *api.Session) error {
	st, err := m.State(ctx, sessionID, sess)
	if err != nil {
		return err
	}
	if st.Reached {
		return st.ReachedError()
	}
	return nil
}

// scopeID resolves the root billing scope.
func (m *Service) scopeID(ctx context.Context, sessionID string, sess *api.Session) string {
	if sess != nil && strings.TrimSpace(sess.ParentSessionID) == "" {
		return sessionID
	}
	if root := tree.RootID(ctx, m.store, sessionID); strings.TrimSpace(root) != "" {
		return root
	}
	return sessionID
}
