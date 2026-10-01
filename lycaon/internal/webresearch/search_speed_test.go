package webresearch

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWholeSearchResultCacheReplay(t *testing.T) {
	resetDirectMemory()
	fake := &FakeDirectDiscoverer{
		Hits: []WebHit{{URL: "https://a.example/widget", Title: "Widget guide", Snippet: "widget guide content"}},
	}
	opts := SearchOptions{
		Query:      "widget frobnicator guide",
		Limit:      2,
		Settings:   DefaultSettings(nil, nil, nil),
		Discoverer: fake,
	}
	r1 := Search(context.Background(), opts)
	if !r1.OK || len(r1.Results) != 1 {
		t.Fatalf("first search = %+v want one hit", r1)
	}
	if fake.Calls != 1 {
		t.Fatalf("discoverer calls = %d want 1", fake.Calls)
	}
	r2 := Search(context.Background(), opts)
	if !r2.OK || !r2.RepeatSearch {
		t.Fatalf("second search = %+v want repeat_search cache hit", r2)
	}
	if fake.Calls != 1 {
		t.Fatalf("discoverer calls = %d want cache skip (still 1)", fake.Calls)
	}
}

func TestWholeSearchResultCacheCanBeBypassedForProviderTests(t *testing.T) {
	resetDirectMemory()
	fake := &FakeDirectDiscoverer{
		Hits: []WebHit{{URL: "https://a.example/widget", Title: "Widget guide", Snippet: "widget guide content"}},
	}
	opts := SearchOptions{
		Query:      "widget provider health probe",
		Limit:      2,
		Settings:   DefaultSettings(nil, nil, nil),
		Discoverer: fake,
	}
	first := Search(context.Background(), opts)
	if !first.OK || len(first.Results) != 1 {
		t.Fatalf("first search = %+v", first)
	}
	fake.Hits = nil
	opts.SkipResultCache = true
	second := Search(context.Background(), opts)
	if len(second.Results) != 0 || second.RepeatSearch {
		t.Fatalf("bypassed search = %+v want live empty result", second)
	}
	if fake.Calls != 2 {
		t.Fatalf("discoverer calls = %d want 2", fake.Calls)
	}
}

func TestWholeSearchResultCacheSeparatesEffectiveProviderConfiguration(t *testing.T) {
	base := Settings{
		EnabledProviders: []string{"direct"},
		SoftProviderIDs:  []string{"hn"},
		Keys:             map[string]string{"hn": "key-one"},
		Config:           map[string]map[string]string{"hn": {"endpoint": "https://one.example"}},
	}
	differentSoftProvider := base
	differentSoftProvider.SoftProviderIDs = []string{"arxiv"}
	differentEndpoint := base
	differentEndpoint.Config = map[string]map[string]string{"hn": {"endpoint": "https://two.example"}}
	differentKey := base
	differentKey.Keys = map[string]string{"hn": "key-two"}
	differentTimeout := base
	differentTimeout.PerProviderTimeoutSec = 9

	key := normalizeSearchResultCacheKey("same query", CurrentPeriod(), 10, base, "")
	if key == normalizeSearchResultCacheKey("same query", CurrentPeriod(), 10, differentSoftProvider, "") {
		t.Fatal("cache key aliases different soft-provider sets")
	}
	if key == normalizeSearchResultCacheKey("same query", CurrentPeriod(), 10, differentEndpoint, "") {
		t.Fatal("cache key aliases different provider endpoints")
	}
	if key == normalizeSearchResultCacheKey("same query", CurrentPeriod(), 10, differentKey, "") {
		t.Fatal("cache key aliases different provider credentials")
	}
	if normalizeSearchResultCacheKey("same query", CurrentPeriod(), 10, base, "hn") ==
		normalizeSearchResultCacheKey("same query", CurrentPeriod(), 10, differentEndpoint, "hn") {
		t.Fatal("explicit-provider cache key aliases different endpoints")
	}
	if normalizeSearchResultCacheKey("same query", CurrentPeriod(), 10, base, "hn") ==
		normalizeSearchResultCacheKey("same query", CurrentPeriod(), 10, differentTimeout, "hn") {
		t.Fatal("explicit-provider cache key aliases different timeouts")
	}
	if normalizeSearchResultCacheKey("same query", CurrentPeriod(), 10, base, directWireProviderID) ==
		normalizeSearchResultCacheKey("same query", CurrentPeriod(), 10, differentEndpoint, directWireProviderID) {
		t.Fatal("pinned Direct cache key aliases different seed-provider endpoints")
	}
	if strings.Contains(key, "key-one") {
		t.Fatal("cache key exposes provider credential")
	}
}

func TestFrontierStrongFloor(t *testing.T) {
	if got := frontierStrongFloor(10); got != 5 {
		t.Fatalf("floor(10) = %d want 5", got)
	}
	if got := frontierStrongFloor(3); got != 1 {
		t.Fatalf("floor(3) = %d want 1", got)
	}
}

func TestFrontierPlateauAfterZeroGainRound(t *testing.T) {
	fr := newFrontier(10, maxHitsPerHost)
	for i := 0; i < frontierStrongFloor(10); i++ {
		fr.hits = append(fr.hits, WebHit{URL: "https://a.example/p" + string(rune('a'+i)), Snippet: "real content"})
	}
	before := fr.strongHits()
	if before < frontierStrongFloor(10) {
		t.Fatalf("strong hits = %d", before)
	}
	// Simulate a frontier round that verified nothing new.
	if after := fr.strongHits(); after >= frontierStrongFloor(fr.maxResults) && after == before {
		fr.plateau = true
	}
	if !fr.plateauReached() {
		t.Fatal("want plateau after zero-gain round at floor")
	}
}

func TestPickSeedsHedgeWinsBeforeSlowPrimary(t *testing.T) {
	resetDirectState(1)
	prevDelay := seedHedgeDelay
	seedHedgeDelay = 20 * time.Millisecond
	t.Cleanup(func() { seedHedgeDelay = prevDelay })

	sum := &hedgeRaceSummarizer{
		slimResponse: `{"seeds":["https://fast.example.com"],"fresh":false,"expand":["widget"],"leads":[]}`,
	}
	d := &directDiscoverer{summarizer: sum, seedSharedProvider: true}
	stats := newSearchStats("t-hedge")
	ctx := withSearchStats(context.Background(), stats)
	plan, err := d.pickSeedsCached(ctx, "widget install guide", CurrentPeriod(), 8, nil, nil)
	if err != nil {
		t.Fatalf("pickSeedsCached: %v", err)
	}
	if len(plan.seeds) == 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if !stats.seedHedge.Load() || !stats.seedHedgeWin.Load() {
		t.Fatalf("hedge=%v hedge_win=%v want both true", stats.seedHedge.Load(), stats.seedHedgeWin.Load())
	}
}

func TestPickSeedsHedgedSkipsRaceWhenDedicated(t *testing.T) {
	resetDirectState(1)

	sum := &scriptedSummarizer{responses: []string{
		`{"seeds":["https://fast.example.com"],"fresh":false,"expand":["widget"],"leads":[]}`,
	}}
	d := &directDiscoverer{
		summarizer:         sum,
		seedSharedProvider: false,
	}
	stats := newSearchStats("t-dedicated")
	ctx := withSearchStats(context.Background(), stats)
	plan, err := d.pickSeedsCached(ctx, "widget install guide", CurrentPeriod(), 8, nil, nil)
	if err != nil {
		t.Fatalf("pickSeedsCached: %v", err)
	}
	if len(plan.seeds) == 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if stats.seedHedge.Load() {
		t.Fatal("want no hedge race when summarizer provider is dedicated")
	}
}

type hedgeRaceSummarizer struct {
	slimResponse string
}

func (h *hedgeRaceSummarizer) Summarize(ctx context.Context, _, user string, _ int) (string, error) {
	slim := slimSeedBudget(8)
	want := fmt.Sprintf("Return %d-%d leads", slim.minLeads, slim.maxLeads)
	if strings.Contains(user, want) {
		return h.slimResponse, nil
	}
	<-ctx.Done()
	return "", ctx.Err()
}
