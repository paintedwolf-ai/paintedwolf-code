package llm

import (
	"strings"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

// ContextLengthLookup returns a model's usable context window from a merged
// ModelEntry when known, without performing live HTTP discovery.
type ContextLengthLookup interface {
	ContextLength(provider, model string) int
}

// Resolve source labels for ResolveTrueWindow.
const (
	WindowResolveExplicit   = "explicit"
	WindowResolveModelEntry = "model_entry"
	WindowResolveMatchTable = "match_table"
	WindowResolveFallback   = "fallback"
)

// ResolveTrueWindow picks the model's physical usable context (true_window):
// an explicit window, then the model entry, the match table, and the fallback.
// No live HTTP.
func ResolveTrueWindow(policy ModelPolicy, lookup ContextLengthLookup, windows modelinfo.ModelContextWindows, cfg compaction.CompactionConfig) (trueWindow int, source string) {
	src := strings.TrimSpace(cfg.WindowSource)
	if src == "" {
		src = compaction.WindowSourceAuto
	}
	if src == compaction.WindowSourceExplicit && cfg.ModelContextWindow > 0 {
		return cfg.ModelContextWindow, WindowResolveExplicit
	}

	provider := strings.TrimSpace(policy.Coordinator.ProviderID)
	model := strings.TrimSpace(policy.Coordinator.Model)
	if lookup != nil && model != "" {
		if n := lookup.ContextLength(provider, model); n > 0 {
			return n, WindowResolveModelEntry
		}
	}
	if n, ok := windows.Lookup(model); ok && n > 0 {
		return n, WindowResolveMatchTable
	}
	fb := windows.FallbackTrueWindow
	if fb < 1 {
		fb = modelinfo.DefaultFallbackTrueWindow
	}
	return fb, WindowResolveFallback
}

// ApplyLiveBudget sets CompactionConfig.ModelContextWindow to the live budget
// (true_window × live_budget_pct, or the explicit window), re-derives token
// ceilings, and stamps MaxWorkerSummaryChars from ScaleLimitsFromTrueWindow
// unless an overlay already set a positive value.
func ApplyLiveBudget(cfg compaction.CompactionConfig, policy ModelPolicy, lookup ContextLengthLookup, windows modelinfo.ModelContextWindows) (compaction.CompactionConfig, SessionLimitFields, error) {
	trueWindow, _ := ResolveTrueWindow(policy, lookup, windows, cfg)

	overlayWorkerSummary := cfg.MaxWorkerSummaryChars

	src := strings.TrimSpace(cfg.WindowSource)
	if src == "" {
		src = compaction.WindowSourceAuto
	}
	if src == compaction.WindowSourceExplicit && cfg.ModelContextWindow > 0 {
		cfg.ModelContextWindow = trueWindow
	} else {
		pct := cfg.LiveBudgetPct
		if pct <= 0 {
			pct = compaction.DefaultLiveBudgetPct
		}
		cfg.ModelContextWindow = trueWindow * pct / 100
	}
	cfg.HardCeilingTokens = 0
	cfg.TargetTokens = 0
	normalized, err := compaction.NormalizeConfig(cfg)
	if err != nil {
		return compaction.CompactionConfig{}, SessionLimitFields{}, err
	}
	limits := ScaleLimitsFromTrueWindow(trueWindow)
	if overlayWorkerSummary > 0 {
		normalized.MaxWorkerSummaryChars = overlayWorkerSummary
	} else {
		normalized.MaxWorkerSummaryChars = limits.MaxWorkerSummaryChars
	}
	return normalized, limits, nil
}

// SessionLimitFields are session/compaction caps derived from a model's true_window.
type SessionLimitFields struct {
	MaxToolResultBytes       int
	MaxCoordinatorLoopCycles int
	MaxWorkerSummaryChars    int
}

// ScaleLimitsFromTrueWindow derives session/compaction limits from true_window.
// max(1, …) is arithmetic safety only — not a product minimum-window policy.
func ScaleLimitsFromTrueWindow(trueWindow int) SessionLimitFields {
	if trueWindow < 1 {
		trueWindow = 1
	}
	return SessionLimitFields{
		MaxToolResultBytes:       trueWindow * 2,
		MaxCoordinatorLoopCycles: max(1, trueWindow/1024),
		MaxWorkerSummaryChars:    max(1, trueWindow/16),
	}
}
