package webresearch

import (
	"context"
	"strings"
	"time"
)

const defaultQueryBackoffMinWords = 3

// queryBackoffProvider wraps REST providers so long agent queries retry with
// trailing words dropped when an index returns no hits.
type queryBackoffProvider struct {
	inner    SearchProvider
	minWords int
}

func (p *queryBackoffProvider) ID() string { return p.inner.ID() }

func (p *queryBackoffProvider) Kind() ProviderKind { return p.inner.Kind() }

func (p *queryBackoffProvider) Configured(s Settings) bool { return p.inner.Configured(s) }

func (p *queryBackoffProvider) Search(ctx context.Context, s Settings, query string, maxResults int) providerOutcome {
	for i, candidate := range backoffQueryVariants(query, p.minWords) {
		candidateCtx := ctx
		// Retry variants may wait for this search's pacing reservation.
		if i > 0 && providerGateWaitFrom(ctx) <= 0 && s.PerProviderTimeoutSec > 0 {
			candidateCtx = withProviderGateWait(ctx, time.Duration(s.PerProviderTimeoutSec)*time.Second)
		}
		out := p.inner.Search(candidateCtx, s, candidate, maxResults)
		if !out.ok || len(out.hits) > 0 {
			return out
		}
	}
	return providerOutcome{providerID: p.ID(), ok: true, reason: "ok", hits: nil}
}

// backoffQueryVariants returns the query followed by progressively shorter
// prefixes down to minWords words.
func backoffQueryVariants(query string, minWords int) []string {
	words := strings.Fields(strings.TrimSpace(query))
	if len(words) == 0 {
		return nil
	}
	if minWords < 1 {
		minWords = 1
	}
	out := make([]string, 0, len(words))
	for n := len(words); n >= minWords; n-- {
		out = append(out, strings.Join(words[:n], " "))
	}
	if len(out) == 0 {
		out = append(out, strings.Join(words, " "))
	}
	return out
}
