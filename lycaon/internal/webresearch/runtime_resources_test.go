package webresearch

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRuntimeProviderCachesStayLocal(t *testing.T) {
	cat := testCatalog(t)
	left, right := NewRegistry(cat, nil), NewRegistry(cat, nil)
	settings := Settings{}
	for _, reg := range []*Registry{left, right} {
		reg.storeProviderSeed("brave", "query", 5, settings, providerOutcome{ok: true, hits: []WebHit{{URL: "https://example.com/hit"}}})
		reg.storeWholeSearch("query", Period{}, 5, settings, "brave", WebSearchResult{OK: true, Results: []WebHit{{URL: "https://example.com/hit"}}})
	}
	Runtime{Registry: left}.InvalidateProvider("brave")
	if _, ok := left.cachedProviderSeed("brave", "query", 5, settings); ok {
		t.Fatal("invalidated runtime retained provider seed")
	}
	if _, ok := left.cachedWholeSearch("query", Period{}, 5, settings, "brave"); ok {
		t.Fatal("invalidated runtime retained search result")
	}
	if _, ok := right.cachedProviderSeed("brave", "query", 5, settings); !ok {
		t.Fatal("provider repair invalidated another runtime")
	}
	if _, ok := right.cachedWholeSearch("query", Period{}, 5, settings, "brave"); !ok {
		t.Fatal("provider repair invalidated another runtime's result")
	}
	fresh := NewRegistry(cat, nil)
	if _, ok := fresh.cachedProviderSeed("brave", "query", 5, settings); ok {
		t.Fatal("new runtime inherited provider seed")
	}
	if _, ok := fresh.cachedWholeSearch("query", Period{}, 5, settings, "brave"); ok {
		t.Fatal("new runtime inherited search result")
	}
}

func TestRuntimeFetchBudgetsStayLocal(t *testing.T) {
	limits := FetchURLLimits{SessionMaxFetches: 1, SessionWindow: time.Hour, HostMaxInFlight: 1}
	left, right := Runtime{FetchBudget: NewFetchBudget(limits)}, Runtime{FetchBudget: NewFetchBudget(limits)}
	release, err := left.FetchBudget.Acquire(context.Background(), "session", "https://example.com")
	testutil.FailErr(t, "reserve left runtime", err)
	defer release()
	otherRelease, err := right.FetchBudget.Acquire(context.Background(), "session", "https://example.com")
	testutil.FailErr(t, "reserve right runtime", err)
	otherRelease()
	if _, err := left.FetchBudget.Acquire(context.Background(), "session", "https://example.com"); err == nil {
		t.Fatal("new runtime reset another runtime's session cap")
	}
}

func TestDiscoveryAdmissionUsesExactReservation(t *testing.T) {
	discovery := NewDiscovery()
	discovery.slots = make(chan struct{}, 1)
	release, ok := discovery.tryAcquire()
	if !ok {
		t.Fatal("first reservation rejected")
	}
	if _, ok := discovery.tryAcquire(); ok {
		t.Fatal("admitted above capacity")
	}
	independent := NewDiscovery()
	other, ok := independent.tryAcquire()
	if !ok {
		t.Fatal("another runtime consumed independent capacity")
	}
	other()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := discovery.acquire(ctx); err != context.Canceled {
		t.Fatalf("canceled acquire = %v", err)
	}
	release()
	next, ok := discovery.tryAcquire()
	if !ok {
		t.Fatal("released capacity unavailable")
	}
	release()
	if _, ok := discovery.tryAcquire(); ok {
		t.Fatal("duplicate release consumed a later reservation")
	}
	next()
}

func TestDiscoveryMemoryStaysLocal(t *testing.T) {
	left, right := NewDiscovery(), NewDiscovery()
	left.recordSeen("session", []WebHit{{URL: "https://example.com/hit"}})
	if len(left.seenURLs("session")) != 1 {
		t.Fatal("recorded hit missing")
	}
	if len(right.seenURLs("session")) != 0 {
		t.Fatal("independent runtime inherited session memory")
	}
	left.storeSeedPlan("query", Period{}, "task", seedPlan{})
	if _, ok := left.cachedSeedPlan("query", Period{}, "task"); !ok {
		t.Fatal("stored plan missing")
	}
	if _, ok := right.cachedSeedPlan("query", Period{}, "task"); ok {
		t.Fatal("independent runtime inherited seed plan")
	}
}

func TestDiscoverySeedAdmissionUsesExactReservation(t *testing.T) {
	discovery := NewDiscovery()
	releases := make([]func(), seedCallMaxParallelShared)
	for i := range releases {
		release, err := discovery.acquireSeed(t.Context(), "provider", true)
		testutil.FailErr(t, "reserve seed call", err)
		releases[i] = release
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := discovery.acquireSeed(ctx, "provider", true); err != context.Canceled {
		t.Fatalf("canceled seed acquire = %v", err)
	}
	independent := NewDiscovery()
	other, err := independent.acquireSeed(t.Context(), "provider", true)
	testutil.FailErr(t, "reserve independent seed call", err)
	other()
	releases[0]()
	next, err := discovery.acquireSeed(t.Context(), "provider", true)
	testutil.FailErr(t, "reserve released seed call", err)
	releases[0]()
	if _, err := discovery.acquireSeed(ctx, "provider", true); err != context.Canceled {
		t.Fatalf("duplicate release freed a later seed call: %v", err)
	}
	next()
	for _, release := range releases {
		release()
	}
}
