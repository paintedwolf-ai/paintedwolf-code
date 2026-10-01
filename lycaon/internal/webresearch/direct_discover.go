package webresearch

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/webindex"
)

const (
	defaultPageBudget = 10
	// directChainTimeout bounds one whole direct chain: memory probes, provider
	// seeds, host crawls, frontier, verification.
	directChainTimeout = 200 * time.Second
	// seedCallTimeoutSec bounds the primary background seed call (warmSeed /
	// pickSeeds). Sized for a slow hosted model streaming a slim plan.
	seedCallTimeoutSec = 60
	// seedSlimRetryTimeoutSec bounds the slim seed retry for background hedge.
	seedSlimRetryTimeoutSec   = 45
	directDiscoverMaxParallel = 2
	// seedCallMaxTokens caps the seed completion used by warmSeed / pickSeeds.
	seedCallMaxTokens = 1536
	// streamParseStride throttles incremental parsing of the streaming seed
	// call: re-parse the accumulated buffer only after this much new content.
	streamParseStride = 192
	// residualHandoffCap bounds the ranked-but-unprobed frontier candidates a
	// finished user search hands to the post-search warm.
	residualHandoffCap = 12
)

// DirectRequest is one direct-pipeline search: the subject, the time window it
// is about, and how many hits to return.
type DirectRequest struct {
	Query string
	// Period is the declared window. Its zero value is the current window.
	Period     Period
	MaxResults int
}

// DirectDiscoverer runs the index-first direct web search for a request.
type DirectDiscoverer interface {
	Search(ctx context.Context, req DirectRequest) ([]WebHit, error)
}

// DirectDiscovererFactory resolves a discoverer per web_search invocation.
type DirectDiscovererFactory func(ctx context.Context, tctx tools.ToolContext) DirectDiscoverer

// FactoryGetter returns the active factory at invocation time.
type FactoryGetter func() DirectDiscovererFactory

type directDiscoverer struct {
	summarizer compaction.Summarizer
	// index is the persistent personal web index; nil disables memory reads
	// and ingestion.
	index *webindex.Store
	// origin stamps everything this discoverer ingests; empty means earned
	// (live search). The warm engine sets OriginWarmed.
	origin string
	// seedOrigin gates provider seed channels: warm skips third-party APIs.
	seedOrigin searchOrigin
	// memoryScope keys the session's seen-URL memory; empty scopes share one
	// global bucket.
	memoryScope string
	// taskHint is the coordinator's active user turn — anchors seed picking when
	// web_search queries drift to adjacent products.
	taskHint string
	registry *Registry
	settings Settings
	// seedProviderID and seedSharedProvider gate seed-call concurrency and
	// hedging: a dedicated summarizer backend gets a higher parallel cap and
	// skips the slim hedge race.
	seedProviderID     string
	seedSharedProvider bool
	// rerank blends the decision engine into verified-page order; the warm
	// engine leaves it zero.
	rerank decide.Reranker
}

// NewDirectDiscovererFactory wires a per-invocation factory for interactive
// Direct Search (memory + provider seeds). index may be nil (no persistent
// memory). Summarizer is not required — warmSeed performs background model seeding.
func NewDirectDiscovererFactory(
	index *webindex.Store,
	wrRegistry *Registry,
	creds *CredentialStore,
	cfg *ConfigStore,
	catalog *Catalog,
	rerank decide.Reranker,
) DirectDiscovererFactory {
	return func(ctx context.Context, tctx tools.ToolContext) DirectDiscoverer {
		if llm.MockEnabled(nil) {
			return nil
		}
		return &directDiscoverer{
			index:       index,
			memoryScope: tctx.SessionID,
			taskHint:    curationctx.TaskHint(ctx),
			registry:    wrRegistry,
			settings:    DefaultSettings(creds, cfg, catalog),
			seedOrigin:  searchOriginUser,
			rerank:      rerank,
		}
	}
}

// Search runs the direct pipeline: seed channels launch in parallel, their
// contributions merge into the frontier incrementally, and memory strong-hits
// can still short-circuit before slower channels finish.
func (d *directDiscoverer) Search(ctx context.Context, req DirectRequest) ([]WebHit, error) {
	if d == nil {
		return nil, fmt.Errorf("discoverer unavailable")
	}
	if req.MaxResults <= 0 {
		req.MaxResults = defaultPageBudget
	}
	start := time.Now()
	stats := statsFrom(ctx)
	hits, memoryFilled, err := d.search(ctx, req, stats)
	attrs := append([]any{
		"query", clipLogText(req.Query),
		"period", req.Period.String(),
		"hits", len(hits),
		"memory_filled", memoryFilled,
		"phase", stats.currentPhase(),
	}, stats.chainAttrs()...)
	if err != nil {
		observability.LogLatency("webresearch", "direct search failed", start, append(attrs, "err", err.Error())...)
		return nil, err
	}
	observability.LogLatency("webresearch", "direct search finished", start, attrs...)
	return hits, nil
}
