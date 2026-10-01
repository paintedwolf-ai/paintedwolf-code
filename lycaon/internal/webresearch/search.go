package webresearch

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/egressgate"
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
type fanTask struct {
	// direct marks the built-in Direct pipeline. Fan-out grace/budget/zero-hit
	// cancel waits for Direct.
	direct bool
	// soft marks Direct-bundled results-only providers. Soft hits never start
	// early-cancel grace and only fuse when primary providers return no hits.
	soft bool
	run  func() providerOutcome
}

func buildSearchTasks(ctx context.Context, explicit, query string, maxResults int, opts SearchOptions) ([]fanTask, []SkippedProvider) {
	if explicit != "" {
		return buildExplicitTasks(ctx, explicit, query, maxResults, opts)
	}
	return buildFanOut(ctx, query, maxResults, opts)
}

// buildExplicitTasks pins the search to one named provider. Explicit selection
// is a host concern (provider test endpoint), so it bypasses the enabled set.
func buildExplicitTasks(ctx context.Context, explicit, query string, maxResults int, opts SearchOptions) ([]fanTask, []SkippedProvider) {
	if isDirectProvider(explicit) {
		return []fanTask{{direct: true, run: directTask(ctx, query, maxResults, opts)}}, nil
	}
	p := opts.Registry.Get(explicit)
	if p == nil {
		return nil, []SkippedProvider{{Provider: explicit, Reason: "not_configured"}}
	}
	if !p.Configured(opts.Settings) {
		return nil, []SkippedProvider{{Provider: explicit, Reason: "auth_missing"}}
	}
	settings := opts.Settings
	catalogQuery := opts.Period.providerQuery(query)
	return []fanTask{{
		run: func() providerOutcome { return p.Search(ctx, settings, catalogQuery, maxResults) },
	}}, nil
}

func directTask(ctx context.Context, query string, maxResults int, opts SearchOptions) func() providerOutcome {
	discoverer := opts.Discoverer
	period := opts.Period
	return func() providerOutcome { return searchDirect(ctx, discoverer, query, period, maxResults) }
}

// buildFanOut runs every enabled provider in parallel: the built-in direct
// pipeline (when enabled) beside configured catalog providers. Soft bundled
// results-only providers run as fallback-only tasks. Outcomes are RRF-fused —
// cross-channel agreement boosts a hit among primary providers.
func buildFanOut(ctx context.Context, query string, maxResults int, opts SearchOptions) ([]fanTask, []SkippedProvider) {
	var tasks []fanTask
	var skipped []SkippedProvider
	settings := opts.Settings
	// Catalog providers take a query string and nothing else, so a declared
	// window rides in their text. The direct pipeline gets it as a value.
	catalogQuery := opts.Period.providerQuery(query)
	directEnabled := false

	appendCatalogTask := func(id string, soft bool) {
		if opts.Registry == nil || !opts.Registry.Has(id) {
			skipped = append(skipped, SkippedProvider{Provider: id, Reason: "not_configured"})
			return
		}
		p := opts.Registry.Get(id)
		if p == nil || !p.Configured(settings) {
			skipped = append(skipped, SkippedProvider{Provider: id, Reason: "not_configured"})
			return
		}
		provider := p
		tasks = append(tasks, fanTask{
			soft: soft,
			run: func() providerOutcome {
				return provider.Search(ctx, settings, catalogQuery, maxResults)
			},
		})
	}

	for _, id := range settings.EnabledProviders {
		if isDirectProvider(id) {
			directEnabled = true
			tasks = append(tasks, fanTask{direct: true, run: directTask(ctx, query, maxResults, opts)})
			continue
		}
		appendCatalogTask(id, false)
	}
	for _, id := range settings.SoftProviderIDs {
		appendCatalogTask(id, true)
	}
	if len(tasks) == 0 && len(skipped) == 0 {
		return nil, []SkippedProvider{{Provider: "search", Reason: "no_providers_enabled"}}
	}
	// The persistent index joins as its own channel only when direct is off;
	// direct already probes memory, and index hits would vote twice.
	if opts.Index != nil && len(tasks) > 0 && !directEnabled {
		index := opts.Index
		tasks = append(tasks, fanTask{
			run: func() providerOutcome {
				return searchIndexMemory(ctx, index, opts.Rerank, query, opts.Period, maxResults)
			},
		})
	}
	return tasks, skipped
}

// fanOutGraceTimeout bounds the wait for more hits after the first result.
var fanOutGraceTimeout = 2 * time.Second

// fanOutZeroHitTimeout bounds a fan-out that has returned no hits.
var fanOutZeroHitTimeout = 4 * time.Second

func runProviderTasks(ctx context.Context, cancel context.CancelFunc, tasks []fanTask, maxResults int) []providerOutcome {
	if len(tasks) == 0 {
		return nil
	}
	if len(tasks) == 1 {
		start := time.Now()
		out := tasks[0].run()
		out.durationMs = time.Since(start).Milliseconds()
		out.hits = sanitizeHits(out.hits)
		return []providerOutcome{out}
	}

	outcomes := make([]providerOutcome, len(tasks))
	done := make([]bool, len(tasks))
	taskCount := len(tasks)
	if taskCount > math.MaxInt32 {
		taskCount = math.MaxInt32
	}
	var pending = int32(taskCount)
	wake := make(chan struct{}, 1)

	// Workers and the coordinator share outcome snapshots under one lock.
	var resultsMu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(len(tasks))
	for i, task := range tasks {
		go func(idx int, fn func() providerOutcome) {
			defer wg.Done()
			taskStart := time.Now()
			out := fn()
			out.durationMs = time.Since(taskStart).Milliseconds()
			// Sanitized once here, ahead of index ingest and the fuse, for every
			// provider.
			out.hits = sanitizeHits(out.hits)
			resultsMu.Lock()
			outcomes[idx] = out
			done[idx] = true
			resultsMu.Unlock()
			atomic.AddInt32(&pending, -1)
			select {
			case wake <- struct{}{}:
			default:
			}
		}(i, task.run)
	}

	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()

	fanoutStart := time.Now()
	graceStart := time.Time{}
	for {
		snapOutcomes, snapDone := snapshotOutcomes(&resultsMu, outcomes, done)
		// No fanCancel while Direct runs, so soft hits cannot abort its
		// crawl/verify pipeline.
		if !directTaskPending(tasks, snapDone) {
			if fanOutBudgetMet(tasks, snapOutcomes, snapDone, maxResults) {
				cancel()
				break
			}
			if fanOutPartialHits(tasks, snapOutcomes, snapDone) > 0 {
				if graceStart.IsZero() {
					graceStart = time.Now()
				} else if fanOutGraceExpired(ctx, time.Since(graceStart)) {
					// Preserve siblings waiting for host approval.
					cancel()
					break
				}
			} else if fanOutZeroHitExpired(ctx, time.Since(fanoutStart)) {
				cancel()
				break
			}
		}
		if atomic.LoadInt32(&pending) == 0 {
			break
		}
		graceLeft := time.Duration(0)
		if !graceStart.IsZero() {
			graceLeft = fanOutGraceTimeout - time.Since(graceStart)
			if graceLeft < 0 {
				graceLeft = 0
			}
		}
		wait := 50 * time.Millisecond
		if graceLeft > 0 && graceLeft < wait {
			wait = graceLeft
		}
		timer := time.NewTimer(wait)
		select {
		case <-finished:
			timer.Stop()
			return outcomes
		case <-wake:
			timer.Stop()
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			cancel()
			<-finished
			return outcomes
		}
	}
	<-finished
	return outcomes
}

func fanOutGraceExpired(ctx context.Context, elapsed time.Duration) bool {
	return egressgate.From(ctx) == nil && elapsed >= fanOutGraceTimeout
}

func fanOutZeroHitExpired(ctx context.Context, elapsed time.Duration) bool {
	return egressgate.From(ctx) == nil && elapsed >= fanOutZeroHitTimeout
}

// snapshotOutcomes copies the fan-out result slices under the lock so the polling
// loop reads a consistent view of what has completed.
func snapshotOutcomes(mu *sync.Mutex, outcomes []providerOutcome, done []bool) ([]providerOutcome, []bool) {
	mu.Lock()
	defer mu.Unlock()
	return append([]providerOutcome(nil), outcomes...), append([]bool(nil), done...)
}

func directTaskPending(tasks []fanTask, done []bool) bool {
	for i, task := range tasks {
		if task.direct && !done[i] {
			return true
		}
	}
	return false
}

func fanOutBudgetMet(tasks []fanTask, outcomes []providerOutcome, done []bool, maxResults int) bool {
	return len(fuseCompletedOutcomes(tasks, outcomes, done, maxResults)) >= maxResults
}

func fanOutPartialHits(tasks []fanTask, outcomes []providerOutcome, done []bool) int {
	return len(fuseCompletedOutcomes(tasks, outcomes, done, len(outcomes)))
}

func fuseCompletedOutcomes(tasks []fanTask, outcomes []providerOutcome, done []bool, maxResults int) []WebHit {
	completed := make([]providerOutcome, 0, len(outcomes))
	for i, o := range outcomes {
		if !done[i] {
			continue
		}
		if i < len(tasks) && tasks[i].soft {
			continue
		}
		completed = append(completed, o)
	}
	return fuseHits(completed, maxResults)
}

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
