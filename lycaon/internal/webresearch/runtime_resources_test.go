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
