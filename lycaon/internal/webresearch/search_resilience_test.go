package webresearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProviderHealthOpensAfterRepeatedHardFailures(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	clock := now
	health := newProviderHealth("dead_api", func() time.Time { return clock })
	inner := &timeoutStubProvider{id: "dead_api"}

	wrap := func() *healthWrappedProvider {
		return &healthWrappedProvider{inner: inner, health: health}
	}

	out := wrap().Search(context.Background(), Settings{}, "probe", 5)
	if out.reason != "timeout" {
		t.Fatalf("first failure = %+v want timeout", out)
	}
	out = wrap().Search(context.Background(), Settings{}, "probe", 5)
	if out.reason != "timeout" {
		t.Fatalf("second failure = %+v want timeout", out)
	}
	out = wrap().Search(context.Background(), Settings{}, "probe", 5)
	if out.reason != "down" {
		t.Fatalf("after repeated failures = %+v want down skip", out)
	}

	clock = clock.Add(providerHealthCooldown + time.Second)
	inner.ok = true
	out = wrap().Search(context.Background(), Settings{}, "probe", 5)
	if !out.ok {
		t.Fatalf("after cooldown = %+v want success", out)
	}
}

func TestFanOutCutsSlowProviderWhenBudgetMet(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(30 * time.Second):
			_, _ = w.Write([]byte(`{"objects":[]}`))
		}
	}))
	t.Cleanup(slow.Close)
	injectProviderHTTPClient(t, slow.Client())

	reg := NewRegistry(testCatalog(t))
	reg.Register(&healthWrappedProvider{
		inner: &stubSeedProvider{
			id:   "fast",
			hits: []WebHit{{URL: "https://example.com/fast", Title: "Fast", Provider: "fast"}},
		},
		health: reg.ensureHealthGate("fast"),
	})
	reg.Register(&healthWrappedProvider{
		inner:  &slowHTTPStubProvider{baseURL: slow.URL},
		health: reg.ensureHealthGate("slow"),
	})

	start := time.Now()
	result := Search(context.Background(), SearchOptions{
		Query: "widget probe",
		Limit: 1,
		Settings: Settings{
			EnabledProviders:      []string{"fast", "slow"},
			PerProviderTimeoutSec: 30,
		},
		Registry: reg,
	})
	if !result.OK || len(result.Results) != 1 || result.Results[0].Provider != "fast" {
		t.Fatalf("result = %+v want one fast hit", result)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("search took %v — slow provider was not cut", elapsed)
	}
}

func TestFanOutGraceCutsStragglersWithPartialHits(t *testing.T) {
	prevGrace := fanOutGraceTimeout
	fanOutGraceTimeout = 200 * time.Millisecond
	t.Cleanup(func() { fanOutGraceTimeout = prevGrace })

	reg := NewRegistry(testCatalog(t))
	reg.Register(&healthWrappedProvider{
		inner: &stubSeedProvider{
			id:   "fast",
			hits: []WebHit{{URL: "https://example.com/partial", Title: "Partial", Provider: "fast"}},
		},
		health: reg.ensureHealthGate("fast"),
	})
	reg.Register(&healthWrappedProvider{
		inner:  &slowSleepStubProvider{delay: 30 * time.Second},
		health: reg.ensureHealthGate("slow"),
	})

	start := time.Now()
	result := Search(context.Background(), SearchOptions{
		Query: "widget probe",
		Limit: 5,
		Settings: Settings{
			EnabledProviders:      []string{"fast", "slow"},
			PerProviderTimeoutSec: 30,
		},
		Registry: reg,
	})
	if !result.OK {
		t.Fatalf("result = %+v want ok partial search", result)
	}
	if len(result.Results) != 1 {
		t.Fatalf("result = %+v want one partial hit", result)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("search took %v — grace did not cut slow provider", elapsed)
	}
}

type slowHTTPStubProvider struct {
	baseURL string
}

func (p *slowHTTPStubProvider) ID() string { return "slow" }

func (p *slowHTTPStubProvider) Kind() ProviderKind { return KindKeyless }

func (p *slowHTTPStubProvider) Configured(Settings) bool { return true }

func (p *slowHTTPStubProvider) Search(ctx context.Context, _ Settings, _ string, _ int) providerOutcome {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL, nil)
	if err != nil {
		return providerOutcome{providerID: p.ID(), reason: "parse_error", detail: err.Error()}
	}
	_, _, err = doProviderHTTP(ctx, req, 30, false)
	if err != nil {
		reason := "parse_error"
		if ctx.Err() != nil {
			reason = "cut"
		}
		return providerOutcome{providerID: p.ID(), reason: reason, detail: err.Error()}
	}
	return providerOutcome{providerID: p.ID(), ok: true, hits: []WebHit{{URL: "https://example.com/slow"}}}
}

type slowSleepStubProvider struct {
	id    string
	delay time.Duration
}

func (p *slowSleepStubProvider) ID() string {
	if p.id != "" {
		return p.id
	}
	return "slow"
}

func (p *slowSleepStubProvider) Kind() ProviderKind { return KindKeyless }

func (p *slowSleepStubProvider) Configured(Settings) bool { return true }

func (p *slowSleepStubProvider) Search(ctx context.Context, _ Settings, _ string, _ int) providerOutcome {
	timer := time.NewTimer(p.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return providerOutcome{providerID: p.ID(), reason: "cut", detail: ctx.Err().Error()}
	case <-timer.C:
		return providerOutcome{providerID: p.ID(), ok: true, hits: []WebHit{{URL: "https://example.com/slow"}}}
	}
}

type timeoutStubProvider struct {
	id string
	ok bool
}

func (p *timeoutStubProvider) ID() string { return p.id }

func (p *timeoutStubProvider) Kind() ProviderKind { return KindKeyless }

func (p *timeoutStubProvider) Configured(Settings) bool { return true }

func (p *timeoutStubProvider) Search(context.Context, Settings, string, int) providerOutcome {
	if p.ok {
		return providerOutcome{providerID: p.id, ok: true, hits: []WebHit{{URL: "https://example.com/recovered"}}}
	}
	return providerOutcome{providerID: p.id, reason: "timeout", detail: "i/o timeout"}
}
