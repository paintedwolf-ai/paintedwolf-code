package cost

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/pricing"
	"github.com/lycaon/lycaon/pkg/api"
)

// UsageSource is who counted the tokens on a recorded call.
type UsageSource string

const (
	// UsageFromProvider marks provider-reported token counts.
	UsageFromProvider        UsageSource = "provider"
	UsageFromProviderPartial UsageSource = "provider_partial"
	// UsageFromHost marks approximate host-counted tokens.
	UsageFromHost UsageSource = "host"
)

// UsageEvent records one LLM call for cost aggregation.
type UsageEvent struct {
	CallCount                  int
	ID                         string
	SessionID                  string
	ParentSessionID            string
	ProjectID                  string
	ProviderID                 string
	Model                      string
	PromptTokens               int
	CompletionTokens           int
	CacheReadInputTokens       int
	CacheCreationInputTokens   int
	CacheCreation1HInputTokens int
	EstimatedNanoUSD           *int64
	Unpriced                   bool
	UnpricedTokens             int
	RateSnapshot               *pricing.Rate
	CacheSavingsNanoUSD        int64
	UnpricedCacheTokens        int
	PricingSource              string
	PricedAsOf                 time.Time
	Caller                     string
	StartedAt                  time.Time
	// UsageSource defaults to UsageFromProvider when empty.
	UsageSource UsageSource
	// NoCharge records the tracker's pricing classification.
	NoCharge bool
}

// CallLedger records provider calls before generation starts.
type CallLedger interface {
	BeginCall(ctx context.Context, evt UsageEvent) error
	// MarkCallUnknown marks a call as possibly billed with unknown usage.
	MarkCallUnknown(ctx context.Context, callID string) error
	// VoidCall discards a call that never reached generation.
	VoidCall(ctx context.Context, callID string) error
}

// Pricer estimates USD cost for a provider/model usage sample.
type Pricer interface {
	EstimateCost(providerID, model string, usage TokenUsage) (CostEstimate, error)
	// NoCharge reports whether the model cannot incur token charges.
	NoCharge(providerID, model string) bool
}

// PricerSetter replaces the active estimation source.
type PricerSetter interface {
	SetPricer(Pricer)
}

// TokenUsage is token counts for estimation.
type TokenUsage struct {
	PromptTokens               int
	CompletionTokens           int
	CacheReadInputTokens       int
	CacheCreationInputTokens   int
	CacheCreation1HInputTokens int
}

// CostEstimate is an estimated cost for a usage sample.
type CostEstimate struct {
	CacheSavingsUSD     float64
	UnpricedCacheTokens int
	EstimatedUSD        float64
	Currency            string
	Unpriced            bool
	UnpricedTokens      int
	PricedTokens        int
	RateSnapshot        *pricing.Rate
	PricingSource       string
	PricedAsOf          time.Time
}

// CostTracker records usage and produces summaries.
type CostTracker interface {
	RecordUsage(ctx context.Context, evt UsageEvent) error
	Estimate(ctx context.Context, providerID, model string, usage TokenUsage) (CostEstimate, error)
	Summary(ctx context.Context, scope api.CostScope, sessionID, projectID string) (api.CostSummary, error)
	ProjectReport(ctx context.Context, projectID string, query ReportQuery) (api.ProjectCostReport, error)
	ClaimSpendWarning(ctx context.Context, sessionID string, ceilingUSD float64) (bool, error)
	ClearSpendWarning(ctx context.Context, sessionID string) error
}
