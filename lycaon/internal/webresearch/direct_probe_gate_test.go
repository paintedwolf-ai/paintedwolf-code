package webresearch

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestProbeEligibleRejectsSeedBonusAlone(t *testing.T) {
	scorer := newQueryScorer("AdGuard Home vs Pi-hole home network DNS best practices 2025", nil, false, CurrentPeriod())
	pool := []indexCandidate{
		{Title: "Web Audio API best practices", URL: "https://developer.mozilla.org/en-US/docs/Web/API/Web_Audio_API/Best_practices", Source: sourceProviderSeed},
		{Title: "AdGuard Home setup on a home network", URL: "https://adguard.com/kb/adguard-home/", Notes: "Pi-hole DNS comparison", Source: sourceProviderSeed},
		{Title: "Unrelated cooking", URL: "https://example.com/recipes", Source: sourceProviderSeed},
	}
	scorer.weighTerms(pool)

	if scorer.probeEligible(pool[0]) {
		t.Fatal("weak best-practices MDN page must not be probe-eligible")
	}
	if !scorer.probeEligible(pool[1]) {
		t.Fatal("on-topic AdGuard/Pi-hole page must be probe-eligible")
	}
	if scorer.probeEligible(pool[2]) {
		t.Fatal("unrelated provider_seed must not clear the probe gate via seed bonus alone")
	}

	ranked := probeWorthy(rankCandidates(pool, scorer), scorer)
	if len(ranked) != 1 || ranked[0].URL != pool[1].URL {
		t.Fatalf("probeWorthy = %+v want only the on-topic URL", ranked)
	}
}

func TestCompactProviderQueryShortensLongQueries(t *testing.T) {
	if got := shortenProviderQuery("dnsmasq"); got != "dnsmasq" {
		t.Fatalf("short query = %q", got)
	}
	long := shortenProviderQuery("best modern local DNS server home network setup 2024 2025 best practices")
	if long == "" || len(strings.Fields(long)) > shortenProviderQueryMaxTerms {
		t.Fatalf("compacted = %q", long)
	}
	if !looksLikeRFCName("rfc9110") || !looksLikeRFCName("RFC 9110") {
		t.Fatal("rfc name detection failed")
	}
	if looksLikeRFCName("HTTP Semantics") {
		t.Fatal("NL title must not look like an RFC name")
	}
}

func TestProviderSeedCacheRoundTrip(t *testing.T) {
	providerSeedCache = newTTLCache[providerOutcome](providerSeedCacheTTL, providerSeedCacheMax)
	out := providerOutcome{
		providerID: "github",
		ok:         true,
		reason:     "ok",
		hits:       []WebHit{{URL: "https://github.com/example/repo", Title: "example"}},
	}
	settings := Settings{
		Keys:   map[string]string{"github": "first"},
		Config: map[string]map[string]string{"github": {"endpoint": "https://one.example"}},
	}
	storeProviderSeed("github", "AdGuard Home", 5, settings, out)
	got, ok := cachedProviderSeed("github", "AdGuard Home", 5, settings)
	if !ok || !got.ok || len(got.hits) != 1 {
		t.Fatalf("cache miss/got = ok=%v %+v", ok, got)
	}
	got.hits[0].URL = "mutated"
	again, _ := cachedProviderSeed("github", "AdGuard Home", 5, settings)
	if again.hits[0].URL != "https://github.com/example/repo" {
		t.Fatal("cached hits must be cloned")
	}
	if _, hit := cachedProviderSeed("github", "AdGuard Home", 5, Settings{Keys: map[string]string{"github": "second"}}); hit {
		t.Fatal("credential changes must select a different cache entry")
	}
	changedEndpoint := Settings{
		Keys:   map[string]string{"github": "first"},
		Config: map[string]map[string]string{"github": {"endpoint": "https://two.example"}},
	}
	if _, hit := cachedProviderSeed("github", "AdGuard Home", 5, changedEndpoint); hit {
		t.Fatal("endpoint changes must select a different cache entry")
	}
	storeProviderSeed("github", "fail", 5, settings, providerOutcome{ok: false, reason: "http_error"})
	if _, hit := cachedProviderSeed("github", "fail", 5, settings); hit {
		t.Fatal("errors must not be cached")
	}
}

func TestProviderHealthTripsOnHTTPError(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	clock := now
	health := newProviderHealth("css_tricks", func() time.Time { return clock })
	inner := &httpErrorStubProvider{id: "css_tricks", status: 404}
	wrap := &healthWrappedProvider{inner: inner, health: health}
	for i := 0; i < providerHealthFailureThreshold; i++ {
		out := wrap.Search(context.Background(), Settings{}, "probe", 5)
		if out.reason != "http_error" {
			t.Fatalf("failure %d = %+v", i, out)
		}
	}
	out := wrap.Search(context.Background(), Settings{}, "probe", 5)
	if out.reason != "down" {
		t.Fatalf("after http_errors = %+v want down", out)
	}
}

func TestProviderHealthResetClosesBreakerImmediately(t *testing.T) {
	health := newProviderHealth("css_tricks", nil)
	health.record(providerOutcome{reason: "http_error"})
	health.record(providerOutcome{reason: "http_error"})
	if !health.isOpen() {
		t.Fatal("breaker did not open")
	}
	health.reset()
	if health.isOpen() {
		t.Fatal("breaker stayed open after provider repair")
	}
}

type httpErrorStubProvider struct {
	id     string
	status int
}

func (p *httpErrorStubProvider) ID() string { return p.id }

func (p *httpErrorStubProvider) Kind() ProviderKind { return KindKeyless }

func (p *httpErrorStubProvider) Configured(Settings) bool { return true }

func (p *httpErrorStubProvider) Search(context.Context, Settings, string, int) providerOutcome {
	return providerOutcome{providerID: p.id, reason: "http_error", httpStatus: p.status, detail: "HTTP 404"}
}
