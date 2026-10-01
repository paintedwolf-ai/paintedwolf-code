package pricing

import (
	"errors"
	"time"
)

// SourceStatus is the feed health projection for settings / ChainPricer.
type SourceStatus string

const (
	StatusOK      SourceStatus = "ok"
	StatusStale   SourceStatus = "stale"
	StatusError   SourceStatus = "error"
	StatusOffline SourceStatus = "offline"
)

const (
	// PricingFetchTimeout bounds one feed fetch including DNS, redirects, and body.
	PricingFetchTimeout = 5 * time.Second
	// PricingCacheTTL is the freshness window before CachedTable marks stale.
	PricingCacheTTL = 24 * time.Hour
	// MaxPricingPayloadBytes caps response bodies (8 MiB).
	MaxPricingPayloadBytes = 8 << 20
)

// Sentinel errors for settings-layer mapping.
var (
	ErrUnreachable = errors.New("pricing source unreachable")
	ErrInvalid     = errors.New("pricing source invalid")
	ErrUnknownKind = errors.New("pricing: unknown source kind")
	ErrUnknownID   = errors.New("pricing: unknown source id")
)

// Rate is USD cost per 1k tokens after normalization.
type Rate struct {
	InputPer1K        *float64     `json:"input_per_1k,omitempty"`
	OutputPer1K       *float64     `json:"output_per_1k,omitempty"`
	CacheReadPer1K    *float64     `json:"cache_read_per_1k,omitempty"`
	CacheWritePer1K   *float64     `json:"cache_write_per_1k,omitempty"`
	CacheWrite1HPer1K *float64     `json:"cache_write_1h_per_1k,omitempty"`
	Currency          string       `json:"currency"`
	Tiers             []PromptTier `json:"tiers,omitempty"`
}

// RateKey combines a catalog provider kind with its canonical API model ID.
type RateKey struct {
	Kind    string
	ModelID string
}

// RateTable is one feed snapshot.
type RateTable struct {
	Rates             map[RateKey]Rate
	FetchedAt         time.Time
	SourceLastUpdated time.Time
	Status            SourceStatus
}

// Scale converts a present price without turning an absent price into zero.
func Scale(value *float64, factor float64) *float64 {
	if value == nil {
		return nil
	}
	v := *value * factor
	return &v
}

// PromptTier applies to the entire call, not just tokens above the threshold.
type PromptTier struct {
	AboveTokens int  `json:"above_tokens"`
	Rate        Rate `json:"rate"`
}

// ForPrompt selects the most specific reported tier. Missing tier prices stay unknown.
func (r Rate) ForPrompt(tokens int) Rate {
	selected := r
	threshold := 0
	for _, tier := range r.Tiers {
		if tokens > tier.AboveTokens && tier.AboveTokens > threshold {
			selected, threshold = tier.Rate, tier.AboveTokens
		}
	}
	selected.Tiers = nil
	selected.InputPer1K = Scale(selected.InputPer1K, 1)
	selected.OutputPer1K = Scale(selected.OutputPer1K, 1)
	selected.CacheReadPer1K = Scale(selected.CacheReadPer1K, 1)
	selected.CacheWritePer1K = Scale(selected.CacheWritePer1K, 1)
	selected.CacheWrite1HPer1K = Scale(selected.CacheWrite1HPer1K, 1)
	return selected
}
