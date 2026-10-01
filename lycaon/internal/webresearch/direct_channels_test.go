package webresearch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/webindex"
)

type stubSeedProvider struct {
	id   string
	hits []WebHit
}

func (p *stubSeedProvider) ID() string { return p.id }

func (p *stubSeedProvider) Kind() ProviderKind { return KindKeyless }

func (p *stubSeedProvider) Configured(Settings) bool { return true }

func (p *stubSeedProvider) Search(_ context.Context, _ Settings, _ string, _ int) providerOutcome {
	return providerOutcome{providerID: p.id, ok: true, reason: "ok", hits: append([]WebHit(nil), p.hits...)}
}

type errSummarizer struct{ err error }

func (e *errSummarizer) Summarize(context.Context, string, string, int) (string, error) {
	if e.err != nil {
		return "", e.err
	}
	return "", fmt.Errorf("seed call failed")
}

var _ compaction.Summarizer = (*errSummarizer)(nil)

func testSeedSettings(enabled ...string) Settings {
	return Settings{
		EnabledProviders: append([]string{}, enabled...),
	}
}

func TestWarmOriginSkipsProviderChannels(t *testing.T) {
	reg := testRegistry(t)
	d := &directDiscoverer{
		registry:   reg,
		settings:   testSeedSettings("hn", "wikipedia"),
		seedOrigin: searchOriginWarm,
	}
	chs := d.constructSeedChannels(seedRequest{Origin: searchOriginWarm})
	for _, ch := range chs {
		if strings.HasPrefix(ch.ID(), channelProviderPrefix) {
			t.Fatalf("warm origin must not construct provider channels, got %s", ch.ID())
		}
	}
	if len(chs) != 1 || chs[0].ID() != channelMemory {
		t.Fatalf("channels = %v want memory only", channelIDs(chs))
	}
}

func TestUserOriginConstructsProviderChannels(t *testing.T) {
	reg := testRegistry(t)
	d := &directDiscoverer{
		registry:   reg,
		settings:   testSeedSettings("hn"),
		seedOrigin: searchOriginUser,
	}
	chs := d.constructSeedChannels(seedRequest{Origin: searchOriginUser})
	var providerCount int
	for _, ch := range chs {
		if ch.ID() == "llm" {
			t.Fatal("user-origin Search must not construct an llm channel")
		}
		if strings.HasPrefix(ch.ID(), channelProviderPrefix) {
			providerCount++
		}
	}
	if providerCount == 0 {
		t.Fatalf("user origin channels = %v want at least one provider channel", channelIDs(chs))
	}
	if len(chs) < 2 || chs[0].ID() != channelMemory {
		t.Fatalf("channels = %v want memory + provider:*", channelIDs(chs))
	}
}

func channelIDs(chs []seedChannel) []string {
	out := make([]string, 0, len(chs))
	for _, ch := range chs {
		out = append(out, ch.ID())
	}
	return out
}

func TestProviderBackedSearchWithoutModel(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	srv := widgetReviewSite(t)
	d := providerBackedDiscoverer(t, "hn", "Widget frobnicator review", srv.URL+"/guide")
	hits, err := d.Search(context.Background(), DirectRequest{Query: "widget frobnicator review", MaxResults: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %+v want provider-backed hit without Summarizer", hits)
	}
	if hits[0].Provider != "hn" {
		t.Fatalf("hit provider = %q want hn", hits[0].Provider)
	}
}

func TestProviderSeedChannelDedupesURL(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Widget frobnicator review</title>
			<meta name="description" content="widget frobnicator review"></head>
			<body>widget frobnicator review</body></html>`)
	}))
	t.Cleanup(srv.Close)

	pageURL := srv.URL + "/guide"
	reg := NewRegistry(testCatalog(t))
	reg.Register(&stubSeedProvider{
		id: "hn",
		hits: []WebHit{{
			Title:   "Widget frobnicator review",
			URL:     pageURL,
			Snippet: "widget frobnicator review API snippet",
		}},
	})

	store, err := webindex.Open(filepath.Join(t.TempDir(), "web-index.db"))
	testutil.FailErr(t, "open index", err)
	t.Cleanup(func() { _ = store.Close() })
	store.QueuePage(t.Context(), webindex.Page{URL: pageURL + "/", Title: "Widget frobnicator review", Verified: true})
	store.Flush()

	d := &directDiscoverer{
		summarizer: &failIfCalledSummarizer{},
		index:      store,
		registry:   reg,
		settings:   testSeedSettings("hn"),
		seedOrigin: searchOriginUser,
	}
	hits, err := d.Search(context.Background(), DirectRequest{Query: "widget frobnicator review", MaxResults: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestAPISnippetFallbackAdmitsRobotsBlocked(t *testing.T) {
	allowLoopbackFetch(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			fmt.Fprint(w, "User-agent: *\nDisallow: /\n")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	scorer := newQueryScorer("github issue widget frobnicator", nil, false, CurrentPeriod())
	hits, _, _ := verifyHits(context.Background(), []indexCandidate{
		{
			Title:         "Widget frobnicator issue",
			URL:           srv.URL + "/issues/1",
			Source:        sourceProviderSeed,
			APISnippet:    "widget frobnicator issue discussion on github",
			APIProviderID: "github",
		},
	}, scorer, 1, maxHitsPerHost)
	if len(hits) != 1 {
		t.Fatalf("hits = %+v want api-snippet fallback", hits)
	}
	if hits[0].Provider != "github" {
		t.Fatalf("provider = %q want github", hits[0].Provider)
	}
	if !strings.Contains(hits[0].Snippet, "widget frobnicator") {
		t.Fatalf("snippet = %q want API text", hits[0].Snippet)
	}
}

func TestAPISnippetFallbackRejects404WithoutRobots(t *testing.T) {
	allowLoopbackFetch(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			fmt.Fprint(w, "User-agent: *\nAllow: /\n")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	scorer := newQueryScorer("widget frobnicator missing", nil, false, CurrentPeriod())
	hits, _, dead := verifyHits(context.Background(), []indexCandidate{
		{
			Title:      "Missing page",
			URL:        srv.URL + "/missing",
			Source:     sourceProviderSeed,
			APISnippet: "widget frobnicator missing page API text",
		},
	}, scorer, 1, maxHitsPerHost)
	if len(hits) != 0 {
		t.Fatalf("hits = %+v want none for 404", hits)
	}
	if len(dead) != 1 {
		t.Fatalf("dead = %v want one dead URL", dead)
	}
}

func TestProviderHitIngestedToIndex(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Widget frobnicator earned</title>
			<meta name="description" content="widget frobnicator earned page"></head>
			<body>widget frobnicator earned</body></html>`)
	}))
	t.Cleanup(srv.Close)

	reg := NewRegistry(testCatalog(t))
	reg.Register(&stubSeedProvider{
		id: "hn",
		hits: []WebHit{{
			Title:   "Widget frobnicator earned",
			URL:     srv.URL + "/earned",
			Snippet: "widget frobnicator earned snippet",
		}},
	})

	store, err := webindex.Open(filepath.Join(t.TempDir(), "web-index.db"))
	testutil.FailErr(t, "open index", err)
	t.Cleanup(func() { _ = store.Close() })

	d := &directDiscoverer{
		index:      store,
		registry:   reg,
		settings:   testSeedSettings("hn"),
		seedOrigin: searchOriginUser,
	}
	if _, err := d.Search(context.Background(), DirectRequest{Query: "widget frobnicator earned", MaxResults: 1}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	store.Flush()
	docs, err := store.Search(context.Background(), "widget frobnicator earned", 5)
	testutil.FailErr(t, "index search", err)
	found := false
	for _, doc := range docs {
		if strings.Contains(doc.URL, "/earned") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("index docs = %+v want ingested provider hit", docs)
	}
}

func TestPacedProviderSeedChannelSoftSkips(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	clock := now
	gate := newProviderGate("test_seed", &PacingSpec{MinIntervalMS: 60_000}, &memQuotaStore{}, func() time.Time { return clock })
	p := &gatedSearchProvider{
		inner: &stubSeedProvider{
			id: "test_seed",
			hits: []WebHit{{
				Title: "Should not run",
				URL:   "https://example.com/x",
			}},
		},
		gate:  gate,
		entry: CatalogEntry{ID: "test_seed", Pacing: &PacingSpec{MinIntervalMS: 60_000}},
	}
	if err := gate.acquire(context.Background(), false); err != nil {
		testutil.FailErr(t, "prime gate", err)
	}
	out := p.Search(context.Background(), Settings{}, "query", 5)
	if out.ok {
		t.Fatal("expected paced skip")
	}
	if out.reason != "paced" {
		t.Fatalf("reason = %q want paced", out.reason)
	}
}

func TestResolveSeedProvidersCatalogDriven(t *testing.T) {
	reg := testRegistry(t)
	ids := resolveSeedProviders(Settings{}, reg)
	if len(ids) == 0 {
		t.Fatal("expected default_enabled seed providers from the catalog")
	}
	cat := reg.Catalog()
	for _, id := range ids {
		entry, ok := cat.Entry(id)
		if !ok {
			t.Fatalf("seed id %q not in catalog", id)
		}
		if !entry.DefaultEnabled || !entry.HasRole(RoleSeeds) {
			t.Fatalf("seed id %q must be default_enabled with seeds role", id)
		}
	}
	// Every results-only catalog entry (no seeds role) must stay out of the seed
	// fan-out. Derived from the catalog so new results-only providers (package
	// registries, structured APIs, etc.) are covered without editing this list.
	seedSet := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		seedSet[id] = struct{}{}
	}
	for _, entry := range cat.Entries() {
		if entry.HasRole(RoleSeeds) {
			continue
		}
		if _, isSeed := seedSet[entry.ID]; isSeed {
			t.Fatalf("results-only provider %q must not appear as a seed channel", entry.ID)
		}
	}
}
