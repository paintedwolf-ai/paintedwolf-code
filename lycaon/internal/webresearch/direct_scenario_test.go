package webresearch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// docSiteHandler serves a minimal doc site: llms.txt with the given body,
// 200 with an empty body elsewhere. pathStatus overrides specific paths.
func docSiteHandler(llmsBody string, pathStatus map[string]int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if code, ok := pathStatus[r.URL.Path]; ok {
			w.WriteHeader(code)
			return
		}
		if r.URL.Path == "/llms.txt" {
			fmt.Fprint(w, llmsBody)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func TestDirectScenarioMultiSiteMixedSeeds(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	alpine := httptest.NewServer(docSiteHandler("", nil))
	t.Cleanup(alpine.Close)
	alpineLLMs := fmt.Sprintf("- [Install](%s/install.md): Alpine Linux installation guide\n", alpine.URL)
	alpine.Config.Handler = docSiteHandler(alpineLLMs, nil)

	talos := httptest.NewServer(docSiteHandler("", nil))
	t.Cleanup(talos.Close)

	flatcar := httptest.NewServer(docSiteHandler("", nil))
	t.Cleanup(flatcar.Close)
	flatcarLLMs := fmt.Sprintf("- [Overview](%s/overview.md): Flatcar Container Linux overview\n", flatcar.URL)
	flatcar.Config.Handler = docSiteHandler(flatcarLLMs, nil)

	directAlpine := alpine.URL + "/install.md"

	d := discovererSeedingURLs(t, directAlpine, alpine.URL, talos.URL, flatcar.URL)
	hits, err := d.Search(context.Background(), DirectRequest{Query: "Alpine Talos Flatcar container linux install", MaxResults: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("hits = %+v want >= 2", hits)
	}
}

func TestDirectScenarioIrrelevantPageSeedRanksBelowIndexMatch(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	srv := httptest.NewServer(docSiteHandler("", nil))
	t.Cleanup(srv.Close)
	goodURL := srv.URL + "/docs/talos-kubernetes.md"
	llms := fmt.Sprintf("- [Talos](%s): Talos Kubernetes immutable OS install\n", goodURL)
	srv.Config.Handler = docSiteHandler(llms, nil)
	irrelevantSeed := srv.URL + "/about.md"

	d := discovererSeedingURLs(t, irrelevantSeed, srv.URL)
	hits, err := d.Search(context.Background(), DirectRequest{Query: "Talos Kubernetes immutable OS", MaxResults: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].URL != goodURL {
		t.Fatalf("hits = %+v want index match to outrank the irrelevant model-named page", hits)
	}
}

// TestDirectScenarioNoMatchQueryErrors pins the no-invention grounding: when
// nothing in any live index carries a rank signal for the query, the search
// errors instead of guessing URLs or surfacing zero-signal pages.
func TestDirectScenarioNoMatchQueryErrors(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	srv := httptest.NewServer(docSiteHandler("", nil))
	t.Cleanup(srv.Close)
	llms := fmt.Sprintf("- [X](%s/x.md): unrelated page\n", srv.URL)
	srv.Config.Handler = docSiteHandler(llms, nil)

	d := discovererSeedingURLs(t, srv.URL)
	if _, err := d.Search(context.Background(), DirectRequest{Query: "zzzyyyy nomatch query", MaxResults: 1}); err == nil {
		t.Fatal("expected error when no candidate carries a rank signal")
	}
}

func TestDirectScenarioEightSeeds(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	seedURLs := make([]string, 8)
	for i := range seedURLs {
		srv := httptest.NewServer(docSiteHandler("", nil))
		t.Cleanup(srv.Close)
		llms := fmt.Sprintf("- [Page](%s/p%d.md): topic%d guide\n", srv.URL, i, i)
		srv.Config.Handler = docSiteHandler(llms, nil)
		seedURLs[i] = srv.URL
	}

	d := discovererSeedingURLs(t, seedURLs...)
	hits, err := d.Search(context.Background(), DirectRequest{Query: "topic3 topic5 guide", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits from multi-site indexes")
	}
}

func TestDirectScenarioParallelSearches(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(2)

	srv := httptest.NewServer(docSiteHandler("", nil))
	t.Cleanup(srv.Close)
	llms := fmt.Sprintf("- [Guide](%s/guide.md): parallel search test topic\n", srv.URL)
	srv.Config.Handler = docSiteHandler(llms, nil)

	makeDiscoverer := func() DirectDiscoverer {
		return discovererSeedingURLs(t, srv.URL)
	}

	const n = 5
	var wg sync.WaitGroup
	results := make([]providerOutcome, n)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			time.Sleep(time.Duration(idx*10) * time.Millisecond)
			results[idx] = searchDirect(ctx, makeDiscoverer(), fmt.Sprintf("parallel search test topic %d", idx), CurrentPeriod(), 3)
		}(i)
	}
	wg.Wait()

	for i, out := range results {
		if !out.ok {
			t.Fatalf("search %d failed: reason=%s detail=%s", i, out.reason, out.detail)
		}
	}
}

func TestDirectScenarioEndToEndSearchFanOut(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	srv := httptest.NewServer(docSiteHandler("", nil))
	t.Cleanup(srv.Close)
	llms := fmt.Sprintf("- [React Hooks](%s/hooks.md): useEffect and useMemo in React\n", srv.URL)
	srv.Config.Handler = docSiteHandler(llms, nil)

	d := discovererSeedingURLs(t, srv.URL)
	result := Search(context.Background(), SearchOptions{
		Query:      "useEffect useMemo react hooks",
		Settings:   Settings{EnabledProviders: []string{"direct"}},
		Discoverer: d,
	})
	if !result.OK || len(result.Results) == 0 {
		t.Fatalf("result = %+v", result)
	}
	if result.Results[0].Provider != "direct" {
		t.Fatalf("provider = %q", result.Results[0].Provider)
	}
}

func TestDirectScenarioFetchFailureDropsHit(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	deadPath := "/dead-page.md"
	srv := httptest.NewServer(docSiteHandler("", nil))
	t.Cleanup(srv.Close)
	llms := fmt.Sprintf("- [Dead](%s%s): useEffect react guide\n", srv.URL, deadPath)
	srv.Config.Handler = docSiteHandler(llms, map[string]int{deadPath: http.StatusNotFound})

	d := discovererSeedingURLs(t, srv.URL)
	_, err := d.Search(context.Background(), DirectRequest{Query: "useEffect react", MaxResults: 1})
	if err == nil {
		t.Fatal("expected error when fetch fails on all candidates")
	}
}

// Sparse sites use their live root as the final probe candidate.
func TestDirectScenarioSparseSiteFallsBackToRoot(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	d := discovererSeedingURLs(t, srv.URL)
	hits, err := d.Search(context.Background(), DirectRequest{Query: "Talos Kubernetes guide", MaxResults: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || strings.TrimSuffix(hits[0].URL, "/") != srv.URL {
		t.Fatalf("hits = %+v want the site root as last resort", hits)
	}
	if !strings.Contains(hits[0].Snippet, "no readable text") {
		t.Fatalf("snippet = %q want thin-content annotation", hits[0].Snippet)
	}
}

func TestParseSeedsJSONScrapesFreestyleSitesKey(t *testing.T) {
	plan, err := parseSeedsJSON(`{"sites":["https://docs.example.com","https://docs.example.com/page"]}`)
	if err != nil || len(plan.seeds) != 2 {
		t.Fatalf("plan = %+v err = %v", plan, err)
	}
}

func TestDirectScenarioSeedLeadsParsed(t *testing.T) {
	plan, err := parseSeedsJSON(`{"seeds":["https://news.example.com"],"fresh":true,"leads":[{"title":"Widget review","host":"news.example.com"}]}`)
	if err != nil {
		t.Fatalf("parseSeedsJSON: %v", err)
	}
	if !plan.fresh || len(plan.leads) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.leads[0].host != "news.example.com" {
		t.Fatalf("lead host = %q", plan.leads[0].host)
	}
	plan.mergeLeadHosts()
	if len(plan.seeds) != 1 || plan.seeds[0] != "https://news.example.com" {
		t.Fatalf("seeds after merge = %+v", plan.seeds)
	}
}

func TestRankSiteLinksPrefersQueryPhrases(t *testing.T) {
	links := []siteIndexLink{
		{URL: "https://news.example.com/about", Title: "about"},
		{URL: "https://news.example.com/games/steam-machine-review", Title: "steam machine review"},
		{URL: "https://news.example.com/contact", Title: "contact"},
	}
	ranked := rankSiteLinks(links, []string{"steam machine review"})
	if !strings.Contains(ranked[0].URL, "steam-machine") {
		t.Fatalf("ranked = %+v", ranked)
	}
}

func TestDirectScenarioSeedPageLinkHarvest(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	review := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><title>Widget frobnicator full review</title><body>widget frobnicator review with benchmarks</body></html>`)
	}))
	t.Cleanup(review.Close)

	roundup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><title>Widget roundup</title><body>
<p>widget frobnicator review coverage this week.</p>
<a href="%s">widget frobnicator review benchmarks and thermals</a>
</body></html>`, review.URL)
	}))
	t.Cleanup(roundup.Close)

	d := discovererSeedingURLs(t, roundup.URL)
	hits, err := d.Search(context.Background(), DirectRequest{Query: "widget frobnicator review", MaxResults: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := review.URL
	if !strings.HasSuffix(want, "/") {
		want += "/"
	}
	found := false
	for _, h := range hits {
		if strings.HasPrefix(h.URL, strings.TrimSuffix(want, "/")) {
			found = true
		}
	}
	if !found {
		t.Fatalf("hits = %+v want seed-page harvested review link", hits)
	}
}

func TestPerHostCapForHostCount(t *testing.T) {
	if perHostCapForHostCount(1) != 5 {
		t.Fatalf("few hosts should allow more per host")
	}
	if perHostCapForHostCount(8) != 2 {
		t.Fatalf("many hosts should tighten cap")
	}
	if perHostCapForHostCount(4) != maxHitsPerHost {
		t.Fatalf("mid host count = default cap")
	}
}

func TestSelectVerifyHitsBreadthFirst(t *testing.T) {
	scorer := newQueryScorer("widget review", nil, false, CurrentPeriod())
	survivors := []scoredVerifyHit{
		{candidate: indexCandidate{URL: "https://a.example/1", Title: "a1"}, content: 0.9},
		{candidate: indexCandidate{URL: "https://a.example/2", Title: "a2"}, content: 0.8},
		{candidate: indexCandidate{URL: "https://b.example/1", Title: "b1"}, content: 0.85},
		{candidate: indexCandidate{URL: "https://c.example/1", Title: "c1"}, content: 0.7},
	}
	for i := range survivors {
		survivors[i].probe = pageProbe{live: true, title: survivors[i].candidate.Title}
	}
	hits := selectVerifyHits(survivors, 3, 3, scorer)
	if len(hits) != 3 {
		t.Fatalf("hits = %d", len(hits))
	}
	hosts := map[string]struct{}{}
	for _, h := range hits {
		hosts[strings.ToLower(urlHost(h.URL))] = struct{}{}
	}
	if len(hosts) < 3 {
		t.Fatalf("want breadth across hosts, got %v", hits)
	}
}

func TestDirectScenarioRobotsDisallowBlocksIndex(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			fmt.Fprint(w, "User-agent: *\nDisallow: /private/\n")
			return
		}
		if r.URL.Path == "/llms.txt" {
			fmt.Fprintf(w, "- [Secret](%s/private/secret.md): useEffect react\n", srvURL(r))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	d := discovererSeedingURLs(t, srv.URL)
	hits, err := d.Search(context.Background(), DirectRequest{Query: "useEffect react", MaxResults: 1})
	if err == nil {
		// The robots-allowed homepage may surface as a root fallback; the
		// disallowed URL itself must never leak into results.
		for _, h := range hits {
			if strings.Contains(h.URL, "/private/") {
				t.Fatalf("hits = %+v leaked robots-disallowed URL", hits)
			}
		}
	}
}

func TestDirectScenarioHubFallbackForIndexlessSite(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	// No llms.txt, no sitemap, no feeds — but the homepage links its docs.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<html><body>
<a href="/docs/getting-started">Getting started with widgets</a>
<a href="/pricing">Pricing</a>
<a href="https://other.example/external">External</a>
</body></html>`)
		case "/docs/getting-started":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><title>Getting started</title><body>widgets guide</body></html>")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	d := discovererSeedingURLs(t, srv.URL)
	hits, err := d.Search(context.Background(), DirectRequest{Query: "widgets getting started", MaxResults: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || !strings.HasSuffix(hits[0].URL, "/docs/getting-started") {
		t.Fatalf("hits = %+v want the hub-scraped docs link", hits)
	}
}

func TestDirectScenarioFreshQueryPrefersRecentFeedItems(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	recent := time.Now().Add(-10 * 24 * time.Hour).Format(time.RFC1123Z)
	old := time.Now().Add(-2 * 365 * 24 * time.Hour).Format(time.RFC1123Z)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed":
			w.Header().Set("Content-Type", "application/rss+xml")
			fmt.Fprintf(w, `<?xml version="1.0"?><rss><channel>
<item><title>Old release notes</title><link>%s/blog/old-release</link><pubDate>%s</pubDate></item>
<item><title>New release notes</title><link>%s/blog/new-release</link><pubDate>%s</pubDate></item>
</channel></rss>`, srvURL(r), old, srvURL(r), recent)
		case "/":
			// Feeds are autodiscovered from the homepage's own declaration,
			// never guessed from well-known paths.
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><head><link rel="alternate" type="application/rss+xml" href="/feed"></head><body>blog</body></html>`)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)

	d := discovererSeedingURLs(t, srv.URL)
	hits, err := d.Search(context.Background(), DirectRequest{Query: "release notes", MaxResults: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %+v want 2", hits)
	}
	if !strings.Contains(hits[0].URL, "new-release") {
		t.Fatalf("first hit = %q want the recent item first", hits[0].URL)
	}
	if !strings.Contains(hits[0].Snippet, "—") {
		t.Fatalf("snippet = %q want a date prefix", hits[0].Snippet)
	}
}
