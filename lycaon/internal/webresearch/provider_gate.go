package webresearch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrProviderPaced is returned when a provider-level gate blocks a call.
var ErrProviderPaced = errors.New("provider paced")

// ProviderQuotaStore persists daily provider call counters.
type ProviderQuotaStore interface {
	ProviderQuotaCount(ctx context.Context, providerID, utcDay string) (int, error)
	ReserveProviderQuota(ctx context.Context, providerID, utcDay string, cap int) (bool, error)
}

type clockFunc func() time.Time

type providerGate struct {
	providerID string
	pacing     PacingSpec
	quota      ProviderQuotaStore
	now        clockFunc

	mu sync.Mutex
	// nextFree serializes reservations at the minimum interval.
	nextFree  time.Time
	coolUntil time.Time
}

type gateWaitCtxKey struct{}

// withProviderGateWait allows a bounded wait for a reserved pacing slot.
func withProviderGateWait(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, gateWaitCtxKey{}, d)
}

func providerGateWaitFrom(ctx context.Context) time.Duration {
	d, _ := ctx.Value(gateWaitCtxKey{}).(time.Duration)
	return d
}

func newProviderGate(providerID string, pacing *PacingSpec, quota ProviderQuotaStore, now clockFunc) *providerGate {
	g := &providerGate{providerID: providerID, quota: quota, now: now}
	if now == nil {
		g.now = time.Now
	}
	if pacing != nil {
		g.pacing = *pacing
	}
	return g
}

// acquire reserves a pacing slot within the caller's wait allowance.
func (g *providerGate) acquire(ctx context.Context, authenticated bool) error {
	if g == nil || authenticated || !g.pacingEnabled() {
		return nil
	}
	maxWait := providerGateWaitFrom(ctx)
	g.mu.Lock()
	now := g.now()
	if !g.coolUntil.IsZero() && now.Before(g.coolUntil) {
		g.mu.Unlock()
		return ErrProviderPaced
	}
	slot := now
	if g.pacing.MinIntervalMS > 0 {
		if g.nextFree.After(slot) {
			slot = g.nextFree
		}
		if slot.Sub(now) > maxWait {
			g.mu.Unlock()
			return ErrProviderPaced
		}
		g.nextFree = slot.Add(time.Duration(g.pacing.MinIntervalMS) * time.Millisecond)
	}
	if g.pacing.DailyCap > 0 && g.quota != nil {
		day := ProviderQuotaDayUTC(now)
		ok, err := g.quota.ReserveProviderQuota(ctx, g.providerID, day, g.pacing.DailyCap)
		if err != nil {
			g.mu.Unlock()
			return err
		}
		if !ok {
			g.mu.Unlock()
			return ErrProviderPaced
		}
	}
	g.mu.Unlock()
	if wait := slot.Sub(g.now()); wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (g *providerGate) record(status int) {
	if g == nil || !g.pacingEnabled() {
		return
	}
	if status != 429 && status != 403 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	cool := g.pacing.Cooldown429S
	if cool <= 0 {
		cool = 900
	}
	g.coolUntil = g.now().Add(time.Duration(cool) * time.Second)
}

func (g *providerGate) resetCooldown() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.coolUntil = time.Time{}
	g.mu.Unlock()
}

func (g *providerGate) pacingEnabled() bool {
	return g.pacing.MinIntervalMS > 0 || g.pacing.DailyCap > 0 || g.pacing.Cooldown429S > 0
}

// ProviderQuotaDayUTC formats the UTC day key for quota counters.
func ProviderQuotaDayUTC(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

func providerAuthenticated(entry CatalogEntry, s Settings) bool {
	slot := entry.CredentialSlot
	if slot == "" {
		slot = entry.OptionalCredentialSlot
	}
	if slot == "" {
		return false
	}
	return strings.TrimSpace(s.Keys[entry.ID]) != ""
}

type gatedSearchProvider struct {
	inner SearchProvider
	gate  *providerGate
	entry CatalogEntry
}

func (p *gatedSearchProvider) ID() string { return p.inner.ID() }

func (p *gatedSearchProvider) Kind() ProviderKind { return p.inner.Kind() }

func (p *gatedSearchProvider) Configured(s Settings) bool { return p.inner.Configured(s) }

func (p *gatedSearchProvider) Search(ctx context.Context, s Settings, query string, maxResults int) providerOutcome {
	if p.gate != nil {
		if err := p.gate.acquire(ctx, providerAuthenticated(p.entry, s)); err != nil {
			if errors.Is(err, ErrProviderPaced) {
				return providerOutcome{
					providerID: p.inner.ID(),
					reason:     "paced",
					detail:     "provider quota or pacing gate",
				}
			}
			return providerOutcome{
				providerID: p.inner.ID(),
				reason:     "parse_error",
				detail:     err.Error(),
			}
		}
	}
	out := p.inner.Search(ctx, s, query, maxResults)
	if p.gate != nil {
		if out.httpStatus > 0 {
			p.gate.record(out.httpStatus)
		}
		if out.gateRecordStatus > 0 {
			p.gate.record(out.gateRecordStatus)
		}
	}
	return out
}
