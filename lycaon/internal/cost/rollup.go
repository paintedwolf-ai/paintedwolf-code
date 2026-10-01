package cost

import (
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// UnknownCalls counts receipts with missing or incomplete observed usage.
type UnknownCalls struct {
	Total int
	// Charged counts unknown calls that may incur charges.
	Charged int
}

// buildCostSummary totals rows already attributed by the query.
func buildCostSummary(evts []UsageEvent, unknown UnknownCalls) api.CostSummary {
	var coordEvents, workerEvents, summarizerEvents []UsageEvent
	for _, evt := range evts {
		switch normalizeCaller(evt.Caller) {
		case CallerCoordinator:
			coordEvents = append(coordEvents, evt)
		case CallerSummarizer:
			summarizerEvents = append(summarizerEvents, evt)
		case CallerWorker:
			workerEvents = append(workerEvents, evt)
		}
	}
	coordinator := breakdownFromEvents(coordEvents)
	workers := breakdownFromEvents(workerEvents)
	summarizer := breakdownFromEvents(summarizerEvents)
	total := mergeBreakdowns(coordinator, workers, summarizer)

	out := api.CostSummary{
		EstimatedNanoUsd: total.EstimatedNanoUsd,
		TokenTotals:      total.TokenTotals,
		Coordinator:      coordinator,
		Workers:          workers,
		Summarizer:       summarizer,
		CacheSavings:     total.CacheSavings,
	}
	allEvents := append(append(append([]UsageEvent{}, coordEvents...), workerEvents...), summarizerEvents...)
	applyPricingProvenance(&out, allEvents)
	out.UnpricedTokens = unpricedTokens(allEvents)
	out.UnknownCalls = unknown.Total
	out.UnknownChargedCalls = unknown.Charged
	for _, evt := range allEvents {
		if evt.UsageSource == UsageFromProviderPartial {
			out.UnknownCalls += max(1, evt.CallCount)
			if !evt.NoCharge {
				out.UnknownChargedCalls += max(1, evt.CallCount)
			}
		}
	}
	out.HostMeasuredTokens = hostMeasuredTokens(allEvents)
	tokenTotal := out.TokenTotals.Prompt + out.TokenTotals.Completion
	out.EstimateCoverage = estimateCoverage(anyPricedEvent(allEvents), tokenTotal, out.UnpricedTokens, out.UnknownChargedCalls)
	return out
}

func anyPricedEvent(evts []UsageEvent) bool {
	for _, evt := range evts {
		if evt.EstimatedNanoUSD != nil {
			return true
		}
	}
	return false
}

// hostMeasuredTokens totals approximate host-counted usage.
func hostMeasuredTokens(evts []UsageEvent) int {
	total := 0
	for _, evt := range evts {
		if evt.UsageSource == UsageFromHost {
			total += evt.PromptTokens + evt.CompletionTokens
		}
	}
	return total
}

// unpricedTokens totals usage without an estimate.
func unpricedTokens(evts []UsageEvent) int {
	total := 0
	for _, evt := range evts {
		if evt.EstimatedNanoUSD == nil {
			total += evt.PromptTokens + evt.CompletionTokens
		} else {
			total += evt.UnpricedTokens
		}
	}
	return total
}

func applyPricingProvenance(out *api.CostSummary, evts []UsageEvent) {
	bySource := make(map[string]time.Time)
	for _, evt := range evts {
		if evt.EstimatedNanoUSD == nil {
			continue
		}
		source := strings.TrimSpace(evt.PricingSource)
		if source == "" {
			source = "unknown"
		}
		current, seen := bySource[source]
		if !seen || evt.PricedAsOf.After(current) {
			bySource[source] = evt.PricedAsOf
		}
	}
	sources := make([]string, 0, len(bySource))
	for source := range bySource {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	out.PricingProvenance = make([]api.CostPricingProvenance, 0, len(sources))
	for _, source := range sources {
		provenance := api.CostPricingProvenance{Source: source}
		if asOf := bySource[source]; !asOf.IsZero() {
			t := asOf.UTC()
			provenance.PricedAt = &t
		}
		out.PricingProvenance = append(out.PricingProvenance, provenance)
	}
}

func breakdownFromEvents(evts []UsageEvent) api.CostBreakdown {
	var prompt, completion, cacheRead, cacheWrite int
	var totalNano int64
	var savings api.CacheSavings
	for _, evt := range evts {
		prompt += evt.PromptTokens
		completion += evt.CompletionTokens
		cacheRead += evt.CacheReadInputTokens
		cacheWrite += evt.CacheCreationInputTokens
		savings.EstimatedNanoUsd += evt.CacheSavingsNanoUSD
		savings.UnpricedTokens += evt.UnpricedCacheTokens
		if evt.EstimatedNanoUSD != nil {
			totalNano += *evt.EstimatedNanoUSD
		}
	}
	out := api.CostBreakdown{
		EstimatedNanoUsd: totalNano,
		TokenTotals: api.TokenTotals{
			Prompt:     prompt,
			CacheRead:  cacheRead,
			CacheWrite: cacheWrite,
			Completion: completion,
		},
	}
	if cacheRead+cacheWrite > 0 {
		out.CacheSavings = &savings
	}
	return out
}

func mergeBreakdowns(parts ...api.CostBreakdown) api.CostBreakdown {
	var out api.CostBreakdown
	for _, part := range parts {
		out.EstimatedNanoUsd += part.EstimatedNanoUsd
		out.TokenTotals.Prompt += part.TokenTotals.Prompt
		out.TokenTotals.Completion += part.TokenTotals.Completion
		out.TokenTotals.CacheRead += part.TokenTotals.CacheRead
		out.TokenTotals.CacheWrite += part.TokenTotals.CacheWrite
		out.TaskCount += part.TaskCount
		if part.CacheSavings != nil {
			if out.CacheSavings == nil {
				out.CacheSavings = &api.CacheSavings{}
			}
			out.CacheSavings.EstimatedNanoUsd += part.CacheSavings.EstimatedNanoUsd
			out.CacheSavings.UnpricedTokens += part.CacheSavings.UnpricedTokens
		}
	}
	return out
}

func CostEventFromSummary(summary api.CostSummary) api.CostEvent {
	return api.CostEvent{
		Scope:            summary.Scope,
		SessionID:        summary.SessionID,
		EstimatedNanoUsd: summary.EstimatedNanoUsd,
		TokenTotals:      summary.TokenTotals,
		Coordinator:      summary.Coordinator,
		Workers:          summary.Workers,
		Summarizer:       summary.Summarizer,
	}
}
