package webresearch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/webindex"
)

// SearchOptions configures a web_search invocation.
type SearchOptions struct {
	Query string
	// Rerank blends the decision engine into verified memory hits.
	Rerank decide.Reranker
	// Period is the declared time window. The zero value is the current window;
	// a past window ranks toward its own years instead of recent ones.
	Period     Period
	Limit      int
	Provider   string // Non-empty pins one provider.
	Settings   Settings
	Discoverer DirectDiscoverer
	Registry   *Registry
	// Index is the persistent web index. It ingests results from every
	// provider and, when the direct pipeline is off, serves as the verified
	// memory channel in the fan-out.
	Index *webindex.Store
	// SessionID attributes log lines and warm activity to the calling session.
	SessionID string
	// SkipResultCache forces live execution for diagnostics.
	SkipResultCache bool
}

// Search fans out to every enabled provider — the built-in direct pipeline
// beside catalog providers — and RRF-fuses their results.
func Search(ctx context.Context, opts SearchOptions) WebSearchResult {
	q := strings.TrimSpace(opts.Query)
	if q == "" {
		return WebSearchResult{
			OK:     false,
			Query:  q,
			Period: opts.Period.String(),
			Error:  "query is required",
		}
	}
	maxResults := opts.Limit
	if maxResults <= 0 {
		maxResults = opts.Settings.MaxResultsDefault
	}
	if maxResults <= 0 {
		maxResults = 10
	}
	explicit := strings.TrimSpace(opts.Provider)
	if err := validateProvider(explicit, opts.Registry); err != nil {
		return WebSearchResult{OK: false, Query: q, Period: opts.Period.String(), Error: err.Error()}
	}

	screened, err := screenOutbound(ctx, secretmatch.SurfaceWebSearch, webSearchDestination(opts.Settings, opts.Registry), q)
	if err != nil {
		return WebSearchResult{OK: false, Period: opts.Period.String(), Error: err.Error()}
	}
	q = screened

	start := time.Now()
	if !opts.SkipResultCache {
		if cached, ok := cachedWholeSearch(q, opts.Period, maxResults, opts.Settings, explicit); ok {
			result := WebSearchResult{
				OK:               true,
				Query:            q,
				Period:           opts.Period.String(),
				Results:          append([]WebHit(nil), cached.hits...),
				ProvidersSkipped: append([]SkippedProvider(nil), cached.providersSkipped...),
				Partial:          cached.partial,
				RepeatSearch:     true,
			}
			stats := newSearchStats(newSearchID())
			logSearchSummary(stats, start, explicit, opts.SessionID, q, nil, result, true)
			return result
		}
	}

	stats := newSearchStats(newSearchID())
	ctx = withSearchStats(ctx, stats)
	fanCtx, fanCancel := context.WithCancel(ctx)
	defer fanCancel()
	tasks, skipped := buildSearchTasks(fanCtx, explicit, q, maxResults, opts)
	if len(tasks) == 0 {
		result := WebSearchResult{
			OK:               true,
			Query:            q,
			Period:           opts.Period.String(),
			ProvidersSkipped: skipped,
		}
		logSearchSummary(stats, start, explicit, opts.SessionID, q, nil, result, false)
		return result
	}

	outcomes := runProviderTasks(fanCtx, fanCancel, tasks, maxResults)
	soft := softProviderSet(opts.Settings.SoftProviderIDs)
	ingestProviderHits(ctx, opts.Index, outcomes, soft)
	for _, s := range skipped {
		outcomes = append(outcomes, providerOutcome{providerID: s.Provider, reason: s.Reason})
	}
	result := finalizeSearchResult(q, maxResults, outcomes, soft)
	result.Period = opts.Period.String()
	result.residualURLs = stats.residuals()
	if strong, maxR, part := stats.directOutcome(); part {
		result.directStrongHits = strong
		result.directMaxResults = maxR
		result.directParticipated = true
	}
	if !opts.SkipResultCache {
		storeWholeSearch(q, opts.Period, maxResults, opts.Settings, explicit, result)
	}
	logSearchSummary(stats, start, explicit, opts.SessionID, q, outcomes, result, false)
	return result
}

// logSearchSummary emits one timing record per search.
func logSearchSummary(stats *searchStats, start time.Time, explicit, sessionID, q string, outcomes []providerOutcome, result WebSearchResult, resultCacheHit bool) {
	attrs := []any{
		"search_id", stats.id(),
		"query", clipLogText(q),
		"hits", len(result.Results),
		"ok", result.OK,
		"partial", result.Partial,
		"result_cache_hit", resultCacheHit,
		"providers", providerTimings(outcomes),
		"duration_ms", time.Since(start).Milliseconds(),
	}
	if explicit != "" {
		attrs = append(attrs, "provider", explicit)
	}
	if sessionID != "" {
		attrs = append(attrs, "session_id", sessionID)
	}
	wrlog.Info("web_search done", attrs...)
}

// providerTimings renders per-provider outcome and duration for the summary
// line: "direct=8340ms/6 memory=1200ms(no_results)".
func providerTimings(outcomes []providerOutcome) string {
	parts := make([]string, 0, len(outcomes))
	for _, o := range outcomes {
		switch {
		case o.ok:
			parts = append(parts, fmt.Sprintf("%s=%dms/%d", o.providerID, o.durationMs, len(o.hits)))
		default:
			parts = append(parts, fmt.Sprintf("%s=%dms(%s)", o.providerID, o.durationMs, o.reason))
		}
	}
	return strings.Join(parts, " ")
}

func validateProvider(provider string, reg *Registry) error {
	if provider == "" {
		return nil
	}
	if isDirectProvider(provider) {
		return nil
	}
	if reg != nil && reg.Has(provider) {
		return nil
	}
	return fmt.Errorf("unknown provider: %s", provider)
}

// fanTask is one parallel provider invocation in a web_search fan-out.
func finalizeSearchResult(q string, maxResults int, outcomes []providerOutcome, soft map[string]struct{}) WebSearchResult {
	skipped := make([]SkippedProvider, 0)
	for _, o := range outcomes {
		if o.ok && len(o.hits) == 0 {
			skipped = append(skipped, SkippedProvider{Provider: o.providerID, Reason: "no_results"})
		}
		if !o.ok {
			skipped = append(skipped, SkippedProvider{Provider: o.providerID, Reason: o.reason})
		}
	}
	primary, softOutcomes := partitionSoftOutcomes(outcomes, soft)
	merged := fuseHits(primary, maxResults)
	// Soft (Direct-bundled results-only) hits fuse only when primary providers
	// return nothing.
	if len(merged) == 0 && len(softOutcomes) > 0 {
		merged = fuseHits(softOutcomes, maxResults)
	}
	partial := false
	for _, o := range outcomes {
		if !o.ok {
			partial = partial || len(merged) > 0
		}
	}
	allSoftEmpty := len(outcomes) > 0
	for _, o := range outcomes {
		if o.ok || !isSoftSkipReason(o.reason) {
			allSoftEmpty = false
			break
		}
	}
	if len(merged) == 0 && allSoftEmpty {
		return WebSearchResult{OK: true, Query: q, ProvidersSkipped: skipped}
	}
	if len(merged) == 0 {
		allFailed := true
		for _, o := range outcomes {
			if o.ok {
				allFailed = false
				break
			}
		}
		if allFailed {
			parts := make([]string, 0, len(outcomes))
			for _, o := range outcomes {
				detail := o.detail
				if detail == "" {
					detail = o.reason
				}
				parts = append(parts, fmt.Sprintf("%s: %s", o.providerID, detail))
			}
			return WebSearchResult{
				OK:               false,
				Query:            q,
				ProvidersSkipped: skipped,
				Error:            strings.Join(parts, "; "),
			}
		}
	}
	return WebSearchResult{
		OK:               true,
		Query:            q,
		Results:          merged,
		ProvidersSkipped: skipped,
		Partial:          partial && len(merged) > 0,
	}
}

func partitionSoftOutcomes(outcomes []providerOutcome, soft map[string]struct{}) (primary, softOnly []providerOutcome) {
	if len(soft) == 0 {
		return outcomes, nil
	}
	primary = make([]providerOutcome, 0, len(outcomes))
	softOnly = make([]providerOutcome, 0)
	for _, o := range outcomes {
		if _, ok := soft[o.providerID]; ok {
			softOnly = append(softOnly, o)
			continue
		}
		primary = append(primary, o)
	}
	return primary, softOnly
}

func isSoftSkipReason(reason string) bool {
	switch reason {
	case "auth_missing", "not_configured", "discoverer_unavailable", "no_sources", "no_providers_enabled", "fetch_failed", "no_results", "paced", "down", "cut":
		return true
	default:
		return false
	}
}
