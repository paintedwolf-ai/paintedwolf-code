package webresearch

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/webindex"
	"strings"
	"sync"
	"time"
)

const (
	// Research sessions issue bursts of rephrased queries; both memories decay
	// on the same horizon as the site index cache.
	seedPlanCacheTTL      = 15 * time.Minute
	seedPlanCacheMax      = 128
	seenMemoryTTL         = 15 * time.Minute
	seenMemoryMaxScopes   = 32
	seenMemoryMaxPerScope = 400
)

// seedPlanCache reuses successful plans for identical query context.
var seedPlanCache = newTTLCache[seedPlan](seedPlanCacheTTL, seedPlanCacheMax)

func normalizeQueryKey(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(query)), " ")
}

// normalizeSeedPlanCacheKey keys the seed-plan cache. Period is in the key:
// the picker ranks one window, so the same words under two windows are two plans.
func normalizeSeedPlanCacheKey(query string, period Period, taskHint string) string {
	q := normalizeQueryKey(query) + "\x00" + period.String()
	h := normalizeQueryKey(taskHint)
	if h == "" {
		return q
	}
	return q + "\x00" + h
}

func cachedSeedPlan(query string, period Period, taskHint string) (seedPlan, bool) {
	return seedPlanCache.get(normalizeSeedPlanCacheKey(query, period, taskHint))
}

func storeSeedPlan(query string, period Period, taskHint string, plan seedPlan) {
	seedPlanCache.put(normalizeSeedPlanCacheKey(query, period, taskHint), plan)
}

// searchMemory tracks URLs already returned to a session so later queries in
// the same burst prefer pages the agent hasn't seen. It is a rank penalty,
// never a filter — a repeat that is genuinely the best answer still wins.
var searchMemory = struct {
	sync.Mutex
	scopes map[string]*seenScope
}{scopes: make(map[string]*seenScope)}

type seenScope struct {
	urls    map[string]time.Time
	touched time.Time
}

// seenURLs returns the canonical URLs already returned to scope.
func seenURLs(scope string) map[string]struct{} {
	searchMemory.Lock()
	defer searchMemory.Unlock()
	entry, ok := searchMemory.scopes[scope]
	if !ok {
		return nil
	}
	out := make(map[string]struct{}, len(entry.urls))
	for u, at := range entry.urls {
		if time.Since(at) > seenMemoryTTL {
			delete(entry.urls, u)
			continue
		}
		out[u] = struct{}{}
	}
	return out
}

func recordSeen(scope string, hits []WebHit) {
	if len(hits) == 0 {
		return
	}
	searchMemory.Lock()
	defer searchMemory.Unlock()
	entry, ok := searchMemory.scopes[scope]
	if !ok {
		if len(searchMemory.scopes) >= seenMemoryMaxScopes {
			oldestKey, oldestAt := "", time.Time{}
			for k, e := range searchMemory.scopes {
				if oldestKey == "" || e.touched.Before(oldestAt) {
					oldestKey, oldestAt = k, e.touched
				}
			}
			delete(searchMemory.scopes, oldestKey)
		}
		entry = &seenScope{urls: make(map[string]time.Time)}
		searchMemory.scopes[scope] = entry
	}
	entry.touched = time.Now()
	for _, h := range hits {
		if len(entry.urls) >= seenMemoryMaxPerScope {
			break
		}
		entry.urls[canonicalURL(h.URL)] = entry.touched
	}
}

func resetDirectMemory() {
	seedPlanCache.reset()
	searchResultCache.reset()
	searchMemory.Lock()
	searchMemory.scopes = make(map[string]*seenScope)
	searchMemory.Unlock()
}

// memoryWireProviderID labels hits served from the persistent web index.
const memoryWireProviderID = "memory"

// sourceIndexMemory tags a candidate that came from the persistent index.
// Verification holds these to a higher relevance bar than live-crawl
// candidates (see relevanceFloor).
const sourceIndexMemory = "index_memory"

// fetchDescriptionLen bounds the text excerpt ingested for a fetched page.
const fetchDescriptionLen = 300

// searchIndexMemory serves the persistent web index as a search channel: FTS
// candidates are live-verified before they appear, and URLs that probe dead are
// evicted. It carries the whole result when every configured provider fails.
func searchIndexMemory(ctx context.Context, index *webindex.Store, rerank decide.Reranker, query string, period Period, maxResults int) providerOutcome {
	outcome := providerOutcome{providerID: memoryWireProviderID}
	if index == nil {
		outcome.reason = "not_configured"
		return outcome
	}
	docs, err := index.Search(ctx, query, maxResults*2+verifyOverfetch)
	if err != nil || len(docs) == 0 {
		outcome.reason = "no_results"
		return outcome
	}
	candidates := make([]indexCandidate, 0, len(docs))
	for _, doc := range docs {
		title := doc.Title
		if title == "" {
			title = doc.Anchors
		}
		candidates = append(candidates, indexCandidate{
			URL:    doc.URL,
			Title:  title,
			Notes:  strings.TrimSpace(strings.Join([]string{doc.Description, doc.Anchors}, " ")),
			Source: sourceIndexMemory,
			Date:   doc.Published,
		})
	}
	scorer := newQueryScorer(query, nil, false, period)
	scorer.rerank = rerank
	scorer.weighTerms(candidates)
	ranked := probeWorthy(rankCandidates(candidates, scorer), scorer)
	if len(ranked) == 0 {
		outcome.reason = "no_results"
		return outcome
	}
	if err := acquireDirectSlot(ctx); err != nil {
		outcome.reason = "timeout"
		return outcome
	}
	defer releaseDirectSlot()
	hits, _, dead := verifyHits(ctx, ranked, scorer, maxResults, maxHitsPerHost)
	for _, url := range dead {
		index.QueueDelete(ctx, url)
	}
	if len(hits) == 0 {
		outcome.reason = "no_results"
		return outcome
	}
	for i := range hits {
		hits[i].Provider = memoryWireProviderID
	}
	outcome.ok = true
	outcome.reason = "ok"
	outcome.hits = hits
	return outcome
}

// ingestProviderHits records custom provider results in the web index as
// unverified pages. Direct (already ingested as verified), memory, and soft
// bundled providers (keyword noise) are excluded.
func ingestProviderHits(ctx context.Context, index *webindex.Store, outcomes []providerOutcome, soft map[string]struct{}) {
	if index == nil {
		return
	}
	for _, o := range outcomes {
		if !o.ok || o.providerID == directWireProviderID || o.providerID == memoryWireProviderID {
			continue
		}
		if _, ok := soft[o.providerID]; ok {
			continue
		}
		for _, h := range o.hits {
			index.QueuePage(ctx, webindex.Page{
				URL:         h.URL,
				Title:       h.Title,
				Description: h.Snippet,
			})
		}
	}
}

// ingestFetchedPage records a page the agent explicitly opened — the
// strongest relevance signal available, from any provider flow.
func ingestFetchedPage(ctx context.Context, index *webindex.Store, res FetchURLResult) {
	if index == nil || res.Status < 200 || res.Status >= 300 {
		return
	}
	desc := strings.Join(strings.Fields(res.Text), " ")
	if len(desc) > fetchDescriptionLen {
		desc = desc[:fetchDescriptionLen]
	}
	index.QueuePage(ctx, webindex.Page{
		URL:         res.URL,
		Title:       res.Title,
		Description: desc,
	})
}

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
			memoryScope: tctx.Identity.SessionID,
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
