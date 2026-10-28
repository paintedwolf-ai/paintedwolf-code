package webresearch

import (
	"context"
	"time"
)

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
