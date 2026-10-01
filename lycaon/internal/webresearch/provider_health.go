package webresearch

import (
	"context"
	"sync"
	"time"
)

const (
	providerHealthFailureThreshold = 2
	providerHealthCooldown         = 30 * time.Minute
)

// providerHealth pauses providers after consecutive hard failures.
type providerHealth struct {
	providerID string
	now        clockFunc

	mu                  sync.Mutex
	consecutiveFailures int
	coolUntil           time.Time
}

func newProviderHealth(providerID string, now clockFunc) *providerHealth {
	h := &providerHealth{providerID: providerID, now: now}
	if now == nil {
		h.now = time.Now
	}
	return h
}

func (h *providerHealth) isOpen() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.coolUntil.IsZero() && h.now().Before(h.coolUntil)
}

func (h *providerHealth) reset() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.consecutiveFailures = 0
	h.coolUntil = time.Time{}
	h.mu.Unlock()
}

func (h *providerHealth) record(out providerOutcome) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if out.ok {
		h.consecutiveFailures = 0
		return
	}
	if !isHardProviderFailure(out) {
		return
	}
	h.consecutiveFailures++
	if h.consecutiveFailures >= providerHealthFailureThreshold {
		h.coolUntil = h.now().Add(providerHealthCooldown)
		h.consecutiveFailures = 0
	}
}

func isHardProviderFailure(out providerOutcome) bool {
	if out.ok {
		return false
	}
	switch out.reason {
	case "timeout", "parse_error", "http_error":
		// Pacing, authentication, and cancellation do not affect health.
		return true
	default:
		return false
	}
}

type healthWrappedProvider struct {
	inner  SearchProvider
	health *providerHealth
}

func (p *healthWrappedProvider) ID() string { return p.inner.ID() }

func (p *healthWrappedProvider) Kind() ProviderKind { return p.inner.Kind() }

func (p *healthWrappedProvider) Configured(s Settings) bool { return p.inner.Configured(s) }

func (p *healthWrappedProvider) Search(ctx context.Context, s Settings, query string, maxResults int) providerOutcome {
	if p.health != nil && p.health.isOpen() {
		return providerOutcome{
			providerID: p.inner.ID(),
			reason:     "down",
			detail:     "provider temporarily skipped after repeated failures",
		}
	}
	out := p.inner.Search(ctx, s, query, maxResults)
	if p.health != nil {
		p.health.record(out)
	}
	return out
}
