package webresearch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/compaction"
)

func TestSearchDirectDiscovererUnavailable(t *testing.T) {
	out := searchDirect(context.Background(), nil, "react hooks", CurrentPeriod(), 5)
	if out.reason != "discoverer_unavailable" {
		t.Fatalf("reason = %q want discoverer_unavailable", out.reason)
	}
}

func TestSearchDirectDiscoverError(t *testing.T) {
	d := &FakeDirectDiscoverer{Err: fmt.Errorf("no seeds")}
	out := searchDirect(context.Background(), d, "useEffect react", CurrentPeriod(), 5)
	if out.reason != "search_error" {
		t.Fatalf("reason = %q want search_error", out.reason)
	}
}

func TestSearchDirectMultipleHits(t *testing.T) {
	d := &FakeDirectDiscoverer{Hits: []WebHit{
		{Title: "useEffect", URL: "https://example.com/useEffect", Snippet: "runs after render", Provider: "direct"},
		{Title: "useMemo", URL: "https://example.com/useMemo", Snippet: "memoizes values", Provider: "direct"},
	}}
	out := searchDirect(context.Background(), d, "useEffect useMemo react", CurrentPeriod(), 5)
	if !out.ok || len(out.hits) != 2 {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestDirectSearchWithoutPersistentIndexDoesNotPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &directDiscoverer{seedOrigin: searchOriginUser}
	_, err := d.Search(ctx, DirectRequest{Query: "react hooks", MaxResults: 1})
	if err == nil {
		t.Fatal("expected canceled search to fail")
	}
}

func TestDirectIndexFirstFastPath(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/llms.txt" {
			fmt.Fprintf(w, "- [Hooks](%s/hooks.md): useEffect runs after render in React\n- [Memo](%s/memo.md): memoizes child renders in React\n", srvURL(r), srvURL(r))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	d := discovererSeedingURLs(t, srv.URL)
	hits, err := d.Search(context.Background(), DirectRequest{Query: "useEffect memo react", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestDirectFrontierProbesIndexCandidatesWithoutExtraModelCall(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/llms.txt" {
			fmt.Fprintf(w, "- [Ref](%s/ref.md): useEffect guide for react hooks\n", srvURL(r))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	d := discovererSeedingURLs(t, srv.URL)
	hits, err := d.Search(context.Background(), DirectRequest{Query: "useEffect react hooks", MaxResults: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %+v want index candidate probed directly", hits)
	}
}

// TestDirectFrontierFollowsAnchors is the discovery-ceiling test: the target
// page is in no site index and was never named by the model — the frontier
// reaches it through an anchor on a page the crawl did find.
func TestDirectFrontierFollowsAnchors(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/llms.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "- [Roundup](%s/widget-frobnicator-roundup): widget frobnicator coverage roundup\n", srv.URL)
	})
	mux.HandleFunc("/widget-frobnicator-roundup", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><head><title>Widget frobnicator roundup</title></head><body>
			<p>All widget frobnicator coverage. Read <a href="%s/deep/widget-frobnicator-review">our full widget frobnicator review here</a>.</p>
			</body></html>`, srv.URL)
	})
	mux.HandleFunc("/deep/widget-frobnicator-review", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>Widget frobnicator review</title>
			<meta name="description" content="Hands-on widget frobnicator review with benchmarks"></head>
			<body><h1>Widget frobnicator review</h1><p>The widget frobnicator impressed us.</p></body></html>`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	d := discovererSeedingURLs(t, srv.URL)
	hits, err := d.Search(context.Background(), DirectRequest{Query: "widget frobnicator review", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, h := range hits {
		if strings.HasSuffix(h.URL, "/deep/widget-frobnicator-review") {
			found = true
		}
	}
	if !found {
		t.Fatalf("hits = %+v want anchor-discovered page", hits)
	}
}

func TestDirectDirectSeedURLFastPath(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	seedURL := srv.URL + "/hooks-useEffect-react.md"
	d := discovererSeedingURLs(t, seedURL)
	hits, err := d.Search(context.Background(), DirectRequest{Query: "useEffect react hooks", MaxResults: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].URL != seedURL {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestParseSeedsJSON(t *testing.T) {
	plan, err := parseSeedsJSON(`{"seeds":["https://a.test","https://b.test/docs/page"],"fresh":true,"expand":["write-ahead logging"]}`)
	if err != nil || len(plan.seeds) != 2 {
		t.Fatalf("plan = %+v err = %v", plan, err)
	}
	if !plan.fresh {
		t.Fatal("fresh flag not parsed")
	}
	if len(plan.expand) != 1 || plan.expand[0] != "write-ahead logging" {
		t.Fatalf("expand = %+v", plan.expand)
	}
	if plan.seeds[0] != "https://a.test" {
		t.Fatalf("base seed = %q", plan.seeds[0])
	}
	if plan.seeds[1] != "https://b.test/docs/page" {
		t.Fatalf("page seed = %q", plan.seeds[1])
	}
}

func TestParseSeedsJSONMissingClosingBrace(t *testing.T) {
	// Small models often close the array but drop the final "}".
	raw := "```json\n{\n\"seeds\": [\n\"https://pkg.go.dev/context\",\n\"https://go.dev/blog/context\"\n]\n```"
	plan, err := parseSeedsJSON(raw)
	if err != nil || len(plan.seeds) != 2 {
		t.Fatalf("plan = %+v err = %v", plan, err)
	}
}

func TestParseSeedsJSONTruncatedMidString(t *testing.T) {
	raw := `{"seeds":["https://a.test/docs","https://b.test/gu`
	plan, err := parseSeedsJSON(raw)
	if err != nil || len(plan.seeds) != 2 {
		t.Fatalf("plan = %+v err = %v", plan, err)
	}
	if plan.seeds[0] != "https://a.test/docs" {
		t.Fatalf("seed = %q", plan.seeds[0])
	}
}

func TestParseSeedsJSONProseFallback(t *testing.T) {
	raw := "Here are some good sources:\n1. https://a.test/docs — official docs.\n2. https://b.test."
	plan, err := parseSeedsJSON(raw)
	if err != nil || len(plan.seeds) != 2 {
		t.Fatalf("plan = %+v err = %v", plan, err)
	}
	if plan.seeds[1] != "https://b.test" {
		t.Fatalf("seed = %q want trailing punctuation stripped", plan.seeds[1])
	}
}

func TestRankCandidates(t *testing.T) {
	scorer := newQueryScorer("useEffect react", nil, false, CurrentPeriod())
	ranked := rankCandidates([]indexCandidate{
		{Title: "Other", URL: "https://ex.com/other"},
		{Title: "React hooks", URL: "https://ex.com/hooks", Notes: "useEffect guide"},
	}, scorer)
	if ranked[0].URL != "https://ex.com/hooks" {
		t.Fatalf("ranked = %+v", ranked)
	}
}

func TestRankCandidatesRecencyAndYearTokens(t *testing.T) {
	recent := time.Now().Add(-30 * 24 * time.Hour)
	old := time.Now().Add(-3 * 365 * 24 * time.Hour)
	scorer := newQueryScorer(fmt.Sprintf("talos comparison %d", time.Now().Year()), nil, true, CurrentPeriod())
	ranked := rankCandidates([]indexCandidate{
		{Title: "Talos review", URL: "https://ex.com/old-talos", Date: old},
		{Title: "Talos review", URL: "https://ex.com/new-talos", Date: recent},
	}, scorer)
	if ranked[0].URL != "https://ex.com/new-talos" {
		t.Fatalf("ranked = %+v want recent first", ranked)
	}
	// The year token matched the recent page's date, not URL text.
	if ranked[0].Score <= ranked[1].Score {
		t.Fatalf("scores = %v vs %v want recent strictly higher", ranked[0].Score, ranked[1].Score)
	}
}

func TestScorerIDFWeightsRareTokens(t *testing.T) {
	// "linux" matches the whole pool, "ignition" only one page — the rare
	// token must dominate the ranking.
	pool := []indexCandidate{
		{Title: "Alpine Linux handbook", URL: "https://a.test/handbook"},
		{Title: "Linux governance", URL: "https://a.test/governance"},
		{Title: "Linux council", URL: "https://a.test/council"},
		{Title: "Ignition provisioning", URL: "https://b.test/ignition"},
	}
	scorer := newQueryScorer("flatcar linux ignition", nil, false, CurrentPeriod())
	scorer.weighTerms(pool)
	ranked := rankCandidates(pool, scorer)
	if ranked[0].URL != "https://b.test/ignition" {
		t.Fatalf("ranked = %+v want ignition page first", ranked)
	}
	// The rare token must outweigh the common one decisively, not by a
	// tiebreak.
	if ranked[0].Score < ranked[1].Score*1.4 {
		t.Fatalf("scores = %v vs %v want a decisive rare-token gap", ranked[0].Score, ranked[1].Score)
	}
}

func TestScorerFuzzyTypoFallback(t *testing.T) {
	// "igniton" is a single-edit typo of "ignition"; the fuzzy alias (shared
	// textrank.WithinOneEdit) must still surface the ignition page, but ranked
	// below an exact-match query so the correction never beats a real hit.
	pool := []indexCandidate{
		{Title: "Alpine Linux handbook", URL: "https://a.test/handbook"},
		{Title: "Ignition provisioning", URL: "https://b.test/ignition"},
	}
	typo := newQueryScorer("igniton", nil, false, CurrentPeriod())
	typo.weighTerms(pool)
	ranked := rankCandidates(pool, typo)
	if ranked[0].URL != "https://b.test/ignition" {
		t.Fatalf("typo query should still surface ignition page: %+v", ranked)
	}
	exact := newQueryScorer("ignition", nil, false, CurrentPeriod())
	exact.weighTerms(pool)
	if typo.rankCandidate(pool[1]) >= exact.rankCandidate(pool[1]) {
		t.Fatalf("fuzzy match %v should be discounted below exact %v",
			typo.rankCandidate(pool[1]), exact.rankCandidate(pool[1]))
	}
}

func TestScorerMatchPhrasesFromExpand(t *testing.T) {
	scorer := newQueryScorer("sqlite wal", []string{"write-ahead logging"}, false, CurrentPeriod())
	if !scorer.anyPhraseIn("sqlite write-ahead logging explained") {
		t.Fatal("expand phrase did not match")
	}
	if scorer.rankCandidate(indexCandidate{
		Title: "WAL primer",
		URL:   "https://example.com/wal",
		Notes: "sqlite write-ahead logging explained",
	}) <= scorer.rankCandidate(indexCandidate{Title: "Unrelated", URL: "https://example.com/other"}) {
		t.Fatal("phrase-matching candidate should outrank unrelated")
	}
}

func TestScorerProbeFieldWeights(t *testing.T) {
	scorer := newQueryScorer("dark mode", nil, false, CurrentPeriod())
	titleHit := scorer.scoreProbe(pageProbe{title: "Dark Mode guide"})
	bodyHit := scorer.scoreProbe(pageProbe{textSample: "dark mode is configured here"})
	if titleHit <= bodyHit {
		t.Fatalf("title score %v should beat body score %v", titleHit, bodyHit)
	}
	// Both carry the exact phrase — the phrase bonus applies to each.
	if titleHit <= scorer.scoreProbe(pageProbe{title: "mode of darkness"}) {
		t.Fatal("phrase match should outscore scattered tokens")
	}
}

func TestScorerBestSentence(t *testing.T) {
	scorer := newQueryScorer("wal checkpoint", nil, false, CurrentPeriod())
	sample := "SQLite is a C-language library. WAL mode improves checkpoint concurrency for writers. Licensing is public domain."
	got := scorer.bestSentence(sample)
	if !strings.Contains(got, "WAL mode improves checkpoint") {
		t.Fatalf("bestSentence = %q", got)
	}
}

func TestParseWebDateLayouts(t *testing.T) {
	cases := []string{
		"2026-05-12",
		"2026-05-12T10:30:00Z",
		"Tue, 12 May 2026 10:30:00 +0000",
		"Tue, 12 May 2026 10:30:00 GMT",
	}
	for _, raw := range cases {
		if parseWebDate(raw).IsZero() {
			t.Fatalf("parseWebDate(%q) = zero", raw)
		}
	}
	if !parseWebDate("not a date").IsZero() {
		t.Fatal("expected zero time for junk input")
	}
}

func TestRobotsDisallow(t *testing.T) {
	body := "User-agent: *\nDisallow: /private/\n"
	if urlAllowedByRobots(body, "https://ex.com/private/secret") {
		t.Fatal("expected disallow")
	}
	if !urlAllowedByRobots(body, "https://ex.com/public") {
		t.Fatal("expected allow")
	}
}

func TestSearchFanOutDirectWhenNoKeys(t *testing.T) {
	d := &FakeDirectDiscoverer{Hits: []WebHit{{Title: "Doc", URL: "https://example.com/doc", Snippet: "guide", Provider: "direct"}}}
	result := Search(context.Background(), SearchOptions{
		Query:      "useEffect react",
		Settings:   Settings{EnabledProviders: []string{"direct"}},
		Discoverer: d,
	})
	if !result.OK || len(result.Results) != 1 || result.Results[0].Provider != "direct" {
		t.Fatalf("result = %+v", result)
	}
}

func TestSearchDirectRunsEvenWhenBraveKeyPresent(t *testing.T) {
	d := &FakeDirectDiscoverer{Hits: []WebHit{{URL: "https://example.com", Provider: "direct"}}}
	result := Search(context.Background(), SearchOptions{
		Query: "test",
		Settings: Settings{
			EnabledProviders: []string{"direct"},
			Keys:             map[string]string{"brave": "key"},
		},
		Discoverer: d,
	})
	if d.Calls != 1 {
		t.Fatalf("calls = %d want 1 in direct mode", d.Calls)
	}
	if !result.OK || len(result.Results) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestParseLLMSTxtLinksLenient(t *testing.T) {
	body := "## Optional\n- [Opt](https://example.com/opt): optional\n## Docs\n- [Hooks](https://example.com/hooks.md): main\n"
	links := parseLLMSTxtLinks(body)
	if len(links) != 1 {
		t.Fatalf("links = %+v", links)
	}
}

func srvURL(r *http.Request) string {
	return "http://" + r.Host
}

type scriptedSummarizer struct {
	responses []string
	idx       int
}

func (s *scriptedSummarizer) Summarize(_ context.Context, _, _ string, _ int) (string, error) {
	if s.idx >= len(s.responses) {
		return `{"hits":[]}`, nil
	}
	out := s.responses[s.idx]
	s.idx++
	return out, nil
}

var _ compaction.Summarizer = (*scriptedSummarizer)(nil)

type failIfCalledSummarizer struct{}

func (f *failIfCalledSummarizer) Summarize(_ context.Context, _, _ string, _ int) (string, error) {
	return "", fmt.Errorf("summarizer must not run during interactive Direct Search")
}

var _ compaction.Summarizer = (*failIfCalledSummarizer)(nil)

func TestVerifyHitsDropsDeadPages(t *testing.T) {
	allowLoopbackFetch(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			fmt.Fprint(w, "<html><title>Live dead-or-alive guide</title><body>dead live page</body></html>")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	hits, _, _ := verifyHits(context.Background(), []indexCandidate{
		{Title: "Dead", URL: srv.URL + "/dead", Source: "sitemap"},
		{Title: "Live", URL: srv.URL + "/ok", Source: "sitemap"},
	}, newQueryScorer("dead live", nil, false, CurrentPeriod()), 5, maxHitsPerHost)
	if len(hits) != 1 || !strings.Contains(hits[0].URL, "/ok") {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].Title != "Live dead-or-alive guide" {
		t.Fatalf("title = %q want page <title>", hits[0].Title)
	}
}

func TestVerifyHitsUsesPageMetadata(t *testing.T) {
	allowLoopbackFetch(t)
	page := `<html><head>
<title>Dark Mode — Tailwind CSS</title>
<meta name="description" content="Using variants to style your site in dark mode.">
<meta property="article:modified_time" content="2026-04-01T00:00:00Z">
</head><body>dark mode docs</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, page)
	}))
	t.Cleanup(srv.Close)

	hits, _, _ := verifyHits(context.Background(), []indexCandidate{
		{URL: srv.URL + "/docs/dark-mode", Source: "sitemap"},
	}, newQueryScorer("tailwind dark mode", nil, false, CurrentPeriod()), 1, maxHitsPerHost)
	if len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].Title != "Dark Mode — Tailwind CSS" {
		t.Fatalf("title = %q", hits[0].Title)
	}
	// The date is a stated field, not snippet prose — an agent reads currency
	// without parsing, and the snippet stays whole for relevance.
	if hits[0].Date != "2026-04-01" {
		t.Fatalf("date = %q want the page's declared modified date", hits[0].Date)
	}
	if !strings.Contains(hits[0].Snippet, "Using variants") {
		t.Fatalf("snippet = %q want meta description", hits[0].Snippet)
	}
	if strings.Contains(hits[0].Snippet, "2026-04-01") {
		t.Fatalf("snippet = %q must not repeat the date field", hits[0].Snippet)
	}
}

func TestVerifyHitsDropsIrrelevantGuessedPages(t *testing.T) {
	allowLoopbackFetch(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><title>premium domain for sale</title><body>Buy this domain today.</body></html>")
	}))
	t.Cleanup(srv.Close)

	// LLM-guessed URL, live page, but the content never mentions the query:
	// the wildcard-200 parked-domain case.
	scorer := newQueryScorer("quantum flux capacitors", nil, false, CurrentPeriod())
	hits, _, _ := verifyHits(context.Background(), []indexCandidate{
		{URL: srv.URL + "/guides/dark-mode", Source: "llm_seed", Title: "Dark mode", Notes: "dark mode guide"},
	}, scorer, 1, maxHitsPerHost)
	if len(hits) != 0 {
		t.Fatalf("hits = %+v want none (irrelevant guessed page)", hits)
	}

	// Same page with index-published provenance still drops when content is irrelevant.
	hits, _, _ = verifyHits(context.Background(), []indexCandidate{
		{URL: srv.URL + "/guides/dark-mode", Source: "sitemap"},
	}, scorer, 1, maxHitsPerHost)
	if len(hits) != 0 {
		t.Fatalf("hits = %+v want none (irrelevant site-published page)", hits)
	}
}

func TestVerifyHitsContentRerank(t *testing.T) {
	allowLoopbackFetch(t)
	// The hub page mentions "wal" once in the body; the specific page carries
	// it in title and headings. Crawl rank puts the hub first (better URL
	// match) — the content re-rank must flip them.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/wal-docs-index":
			fmt.Fprint(w, `<html><title>Documentation index</title><body>All docs. See wal notes somewhere.</body></html>`)
		case "/write-ahead-log":
			fmt.Fprint(w, `<html><title>WAL mode</title><body><h1>WAL checkpoint behavior</h1>wal concurrency details</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	hits, _, _ := verifyHits(context.Background(), []indexCandidate{
		{URL: srv.URL + "/wal-docs-index", Source: "sitemap"},
		{URL: srv.URL + "/write-ahead-log", Source: "sitemap"},
	}, newQueryScorer("wal checkpoint", nil, false, CurrentPeriod()), 2, maxHitsPerHost)
	if len(hits) != 2 {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].Title != "WAL mode" {
		t.Fatalf("first hit = %q want the content-rich page first", hits[0].Title)
	}
}

func TestVerifyHitsHostDiversityCap(t *testing.T) {
	allowLoopbackFetch(t)
	page := func(title string) string {
		return fmt.Sprintf("<html><title>%s</title><body>widget guide content</body></html>", title)
	}
	rich := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, page("Widget guide "+r.URL.Path))
	}))
	t.Cleanup(rich.Close)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, page("Widget guide elsewhere"))
	}))
	t.Cleanup(other.Close)

	picks := make([]indexCandidate, 0, 6)
	for i := 0; i < 5; i++ {
		picks = append(picks, indexCandidate{URL: fmt.Sprintf("%s/g%d", rich.URL, i), Source: "sitemap"})
	}
	picks = append(picks, indexCandidate{URL: other.URL + "/guide", Source: "sitemap"})

	hits, _, _ := verifyHits(context.Background(), picks, newQueryScorer("widget guide", nil, false, CurrentPeriod()), 4, maxHitsPerHost)
	if len(hits) != 4 {
		t.Fatalf("hits = %+v", hits)
	}
	otherHost := 0
	for _, h := range hits {
		if strings.HasPrefix(h.URL, other.URL) {
			otherHost++
		}
	}
	if otherHost != 1 {
		t.Fatalf("hits = %+v want the second host represented despite lower rank", hits)
	}
}

func TestVerifyHitsHostCapDropsNotBackfills(t *testing.T) {
	allowLoopbackFetch(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<html><title>Widget guide %s</title><body>widget guide content</body></html>", r.URL.Path)
	}))
	t.Cleanup(srv.Close)

	picks := make([]indexCandidate, 0, 5)
	for i := 0; i < 5; i++ {
		picks = append(picks, indexCandidate{URL: fmt.Sprintf("%s/g%d", srv.URL, i), Source: "sitemap"})
	}
	// One host, room for five: the cap must shrink the result set, not defer
	// the excess into it.
	hits, _, _ := verifyHits(context.Background(), picks, newQueryScorer("widget guide", nil, false, CurrentPeriod()), 5, maxHitsPerHost)
	if len(hits) != maxHitsPerHost {
		t.Fatalf("hits = %d want %d (capped, no backfill)", len(hits), maxHitsPerHost)
	}
}

func TestVerifyHitsAnnotatesThinJSPage(t *testing.T) {
	allowLoopbackFetch(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Widget guide</title></head><body><div id="app"></div></body></html>`)
	}))
	t.Cleanup(srv.Close)

	hits, _, _ := verifyHits(context.Background(), []indexCandidate{
		{URL: srv.URL + "/app", Source: "sitemap"},
	}, newQueryScorer("widget guide", nil, false, CurrentPeriod()), 1, maxHitsPerHost)
	if len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	if !strings.Contains(hits[0].Snippet, thinContentNote) {
		t.Fatalf("snippet = %q want thin-content note", hits[0].Snippet)
	}
}

func TestProbeUsesReadabilityContentNotChrome(t *testing.T) {
	allowLoopbackFetch(t)
	// Nav chrome carries a query token and a wordy promo anchor; only the
	// article's sentence and the article's anchor may surface.
	page := `<html><head><title>Widget frobnicator review</title></head><body>
<nav>Sign in Store Home Discovery Queue Wishlist Points Shop
<a href="/nav-promo">Shop the big widget seasonal sale today</a></nav>
<article>
<p>The widget frobnicator review found the device impressively quiet and small under a TV.</p>
<p>Benchmarks show the widget frobnicator trailing similarly priced mini PCs by a wide margin, and its fan curve stays inaudible until sustained load.</p>
<p>Storage is tight on the base model, so factor an external drive into the widget frobnicator price before deciding.</p>
<p>Read <a href="/full-benchmarks">the full widget frobnicator benchmark data</a> for per-game numbers.</p>
</article></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, page)
	}))
	t.Cleanup(srv.Close)

	scorer := newQueryScorer("widget frobnicator review", nil, false, CurrentPeriod())
	hits, expand, _ := verifyHits(context.Background(), []indexCandidate{
		{URL: srv.URL + "/review", Source: "sitemap"},
	}, scorer, 1, maxHitsPerHost)
	if len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	if strings.Contains(hits[0].Snippet, "Sign in") || strings.Contains(hits[0].Snippet, "Discovery Queue") {
		t.Fatalf("snippet = %q leaked nav chrome", hits[0].Snippet)
	}
	if !strings.Contains(hits[0].Snippet, "quiet and small") {
		t.Fatalf("snippet = %q want the article sentence", hits[0].Snippet)
	}
	var expandURLs []string
	for _, c := range expand {
		expandURLs = append(expandURLs, c.URL)
		if strings.Contains(c.URL, "nav-promo") {
			t.Fatalf("expand = %+v harvested a nav link", expand)
		}
	}
	if len(expandURLs) != 1 || !strings.HasSuffix(expandURLs[0], "/full-benchmarks") {
		t.Fatalf("expand = %+v want only the in-article anchor", expand)
	}
}

func TestDirectSitePinnedQueryUsesMemoryOnlyChannels(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	var llms strings.Builder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/llms.txt" {
			fmt.Fprint(w, llms.String())
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<html><title>Widget guide %s</title><body>widget guide content</body></html>", r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&llms, "- [Guide %d](%s/guide%d.md): widget guide part %d\n", i, srv.URL, i, i)
	}

	d := &directDiscoverer{summarizer: &failIfCalledSummarizer{}}
	hits, err := d.Search(context.Background(), DirectRequest{Query: fmt.Sprintf("site:%s widget guide", srv.URL), MaxResults: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// A pinned host is exempt from the diversity cap — one host is the point.
	if len(hits) != 5 {
		t.Fatalf("hits = %d want 5 (per-host cap disabled)", len(hits))
	}
}

func TestDirectLinkExpansionReachesUnseededHost(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	review := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><title>Widget frobnicator full review</title><body>widget frobnicator review with benchmarks</body></html>`)
	}))
	t.Cleanup(review.Close)

	news := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/llms.txt":
			fmt.Fprintf(w, "- [Roundup](%s/roundup): widget frobnicator review roundup\n", srvURL(r))
		case "/roundup":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<html><title>Widget frobnicator review roundup</title><body>
widget frobnicator review coverage.
<a href="%s/full-review">Read the widget frobnicator full review</a>
<a href="/nav">Nav</a>
</body></html>`, review.URL)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(news.Close)

	d := discovererSeedingURLs(t, news.URL)
	hits, err := d.Search(context.Background(), DirectRequest{Query: "widget frobnicator review", MaxResults: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// The review host was never seeded — only the roundup's in-content anchor
	// can reach it.
	found := false
	for _, h := range hits {
		if h.URL == review.URL+"/full-review" {
			found = true
		}
	}
	if !found {
		t.Fatalf("hits = %+v want link-expanded %s/full-review", hits, review.URL)
	}
}

func TestSeedPlanCacheSkipsRepeatLLMCalls(t *testing.T) {
	resetDirectState(1)

	first := &scriptedSummarizer{responses: []string{`{"seeds":["https://example.com"]}`}}
	d := &directDiscoverer{summarizer: first, seedSharedProvider: true}
	plan, err := d.pickSeedsCached(context.Background(), "useEffect hooks cache", CurrentPeriod(), 8, nil, nil)
	if err != nil {
		t.Fatalf("first pickSeedsCached: %v", err)
	}
	if len(plan.seeds) == 0 {
		t.Fatalf("plan = %+v want seeds", plan)
	}
	if first.idx != 1 {
		t.Fatalf("first LLM calls = %d want 1", first.idx)
	}

	// Same query modulo case/whitespace, fresh discoverer with nothing
	// scripted: only the plan cache can produce seeds.
	second := &scriptedSummarizer{}
	d2 := &directDiscoverer{summarizer: second, seedSharedProvider: true}
	plan2, err := d2.pickSeedsCached(context.Background(), "useEffect Hooks   cache", CurrentPeriod(), 8, nil, nil)
	if err != nil {
		t.Fatalf("second pickSeedsCached: %v", err)
	}
	if len(plan2.seeds) == 0 {
		t.Fatalf("plan2 = %+v want cached seeds", plan2)
	}
	if second.idx != 0 {
		t.Fatalf("second LLM calls = %d want 0 (seed plan cached)", second.idx)
	}
}

func TestSeenMemoryPenalizesRepeats(t *testing.T) {
	resetDirectMemory()
	recordSeen("scope-a", []WebHit{{URL: "https://ex.com/seen"}})

	scorer := newQueryScorer("widget guide", nil, false, CurrentPeriod())
	scorer.markSeen(seenURLs("scope-a"))
	ranked := rankCandidates([]indexCandidate{
		{Title: "Widget guide", URL: "https://ex.com/seen"},
		{Title: "Widget guide", URL: "https://ex.com/fresh"},
	}, scorer)
	if ranked[0].URL != "https://ex.com/fresh" {
		t.Fatalf("ranked = %+v want the unseen page first", ranked)
	}
	// Other scopes are unaffected.
	if urls := seenURLs("scope-b"); len(urls) != 0 {
		t.Fatalf("scope-b seen = %+v want empty", urls)
	}
}

func TestSiteIndexCacheReusedAcrossQueries(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	llmsFetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/llms.txt" {
			llmsFetches++
			fmt.Fprintf(w, "- [Hooks](%s/hooks.md): useEffect runs after render in React\n", srvURL(r))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	for i := 0; i < 3; i++ {
		d := discovererSeedingURLs(t, srv.URL)
		hits, err := d.Search(context.Background(), DirectRequest{Query: "useEffect react", MaxResults: 3})
		if err != nil || len(hits) == 0 {
			t.Fatalf("query %d: hits = %+v err = %v", i, hits, err)
		}
	}
	if llmsFetches != 1 {
		t.Fatalf("llms.txt fetched %d times want 1 (cache)", llmsFetches)
	}
}

// TestDirectSearchRecordsOutcomeForStarvedQuery: a user-origin search that
// ends with fewer content-bearing hits than asked records its outcome, which
// queues the query for the scheduled starved re-warm.
func TestDirectSearchRecordsOutcomeForStarvedQuery(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/llms.txt" {
			fmt.Fprintf(w, "- [Hooks](%s/hooks.md): useEffect runs after render in React\n", srvURL(r))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	index := testIndex(t)
	d := discovererSeedingURLsWithIndex(t, index, srv.URL)
	if _, err := d.Search(context.Background(), DirectRequest{Query: "useEffect react hooks", MaxResults: 10}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	index.Flush()
	starved, err := index.StarvedQueries(context.Background(), 5)
	if err != nil {
		t.Fatalf("starved: %v", err)
	}
	if len(starved) != 1 || starved[0].Query != "useEffect react hooks" {
		t.Fatalf("starved = %v want the underfilled query recorded", starved)
	}
}
