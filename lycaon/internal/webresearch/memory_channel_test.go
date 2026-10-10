package webresearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/webindex"
)

func testIndex(t *testing.T) *webindex.Store {
	t.Helper()
	store, err := webindex.Open(t.Context(), filepath.Join(t.TempDir(), "web-index.db"))
	testutil.FailErr(t, "open index", err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func braveTestServer(t *testing.T, hits ...map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"web": map[string]any{"results": hits},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func customBraveOptions(t *testing.T, srv *httptest.Server, index *webindex.Store) SearchOptions {
	t.Helper()
	spec := braveRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?q=" + url.QueryEscape(query), nil
	}
	reg := NewRegistry(testCatalog(t))
	reg.Register(NewRESTSearchProvider(spec, KindKeyed))
	injectProviderHTTPClient(t, srv.Client())
	return SearchOptions{
		Settings: Settings{
			Keys:             map[string]string{"brave": "brave-key"},
			EnabledProviders: []string{"brave"},
		},
		Registry: reg,
		Index:    index,
	}
}

func TestCustomModeIngestsProviderHits(t *testing.T) {
	resetDirectState(1)
	index := testIndex(t)
	srv := braveTestServer(t, map[string]string{
		"url": "https://news.example/widget-frobnicator-review", "title": "Widget frobnicator review", "description": "hands-on",
	})
	opts := customBraveOptions(t, srv, index)
	opts.Query = "widget frobnicator review"

	result := Search(context.Background(), opts)
	if !result.OK || len(result.Results) == 0 {
		t.Fatalf("result = %+v", result)
	}
	index.Flush()
	docs, err := index.Search(context.Background(), "widget frobnicator review", 5)
	testutil.FailErr(t, "index search", err)
	if len(docs) != 1 || docs[0].Verified {
		t.Fatalf("docs = %+v want one unverified provider-claimed page", docs)
	}
}

// TestCustomModeMemoryCarriesResultWhenProviderFails is the fallback: the
// provider rate-limits, the remembered page (live-verified) answers anyway.
func TestCustomModeMemoryCarriesResultWhenProviderFails(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)
	index := testIndex(t)

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Widget frobnicator review</title>
			<meta name="description" content="widget frobnicator review with benchmarks"></head>
			<body><h1>Widget frobnicator review</h1></body></html>`)
	}))
	t.Cleanup(page.Close)
	index.QueuePage(t.Context(), webindex.Page{URL: page.URL + "/", Title: "Widget frobnicator review", Verified: true})
	index.Flush()

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(provider.Close)
	opts := customBraveOptions(t, provider, index)
	opts.Query = "widget frobnicator review"

	result := Search(context.Background(), opts)
	if !result.OK || len(result.Results) == 0 {
		t.Fatalf("result = %+v want memory to carry the result", result)
	}
	if result.Results[0].Provider != memoryWireProviderID {
		t.Fatalf("provider = %q want %q", result.Results[0].Provider, memoryWireProviderID)
	}
	if !strings.HasPrefix(result.Results[0].URL, page.URL) {
		t.Fatalf("url = %q want remembered page", result.Results[0].URL)
	}
}

// TestSearchIndexMemoryDropsLowConfidenceMatch is the empty-provider trap: a
// live, remembered page that only shares generic tokens ("best", "libraries")
// with a long, specific query must not become the answer just because the
// configured providers returned nothing.
func TestSearchIndexMemoryDropsLowConfidenceMatch(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)
	index := testIndex(t)

	offtopic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Best Python libraries for Raspberry Pi GPIO</title>
			<meta name="description" content="A roundup of the best Python libraries for single board computers."></head>
			<body><h1>Best Python libraries</h1><p>Raspberry Pi GPIO and I2C control for education.</p></body></html>`)
	}))
	t.Cleanup(offtopic.Close)
	index.QueuePage(t.Context(), webindex.Page{
		URL:      offtopic.URL + "/",
		Title:    "Best Python libraries for Raspberry Pi GPIO",
		Verified: true,
	})
	index.Flush()

	outcome := searchIndexMemory(context.Background(), index, decide.Reranker{}, "best web based chess libraries javascript react vue html5", CurrentPeriod(), 5)
	if outcome.ok {
		t.Fatalf("outcome = %+v want low-confidence memory match dropped", outcome)
	}
}

// TestSearchIndexMemoryKeepsOnTopicMatch is the counterpart: a remembered page
// that matches most of the same long query clears the memory relevance bar.
func TestSearchIndexMemoryKeepsOnTopicMatch(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)
	index := testIndex(t)

	ontopic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Best chess libraries for JavaScript and React</title>
			<meta name="description" content="Web-based chess libraries for JavaScript, React, Vue, and HTML5 boards."></head>
			<body><h1>Chess libraries for the web</h1></body></html>`)
	}))
	t.Cleanup(ontopic.Close)
	index.QueuePage(t.Context(), webindex.Page{
		URL:      ontopic.URL + "/",
		Title:    "Best chess libraries for JavaScript and React",
		Verified: true,
	})
	index.Flush()

	outcome := searchIndexMemory(context.Background(), index, decide.Reranker{}, "best web based chess libraries javascript react vue html5", CurrentPeriod(), 5)
	if !outcome.ok || len(outcome.hits) == 0 {
		t.Fatalf("outcome = %+v want on-topic memory hit kept", outcome)
	}
}

func TestSearchIndexMemoryVerifiesAndEvictsDead(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)
	index := testIndex(t)

	dead := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(dead.Close)
	deadURL := dead.URL + "/gone-widget-frobnicator"
	index.QueuePage(t.Context(), webindex.Page{URL: deadURL, Title: "widget frobnicator guide"})
	index.Flush()

	outcome := searchIndexMemory(context.Background(), index, decide.Reranker{}, "widget frobnicator guide", CurrentPeriod(), 3)
	if outcome.ok {
		t.Fatalf("outcome = %+v want no hits from a dead memory", outcome)
	}
	index.Flush()
	docs, err := index.Search(context.Background(), "widget frobnicator guide", 5)
	testutil.FailErr(t, "index search", err)
	if len(docs) != 0 {
		t.Fatalf("docs = %+v want dead URL evicted", docs)
	}
}

func TestSearchIndexMemoryNilAndEmpty(t *testing.T) {
	resetDirectState(1)
	if o := searchIndexMemory(context.Background(), nil, decide.Reranker{}, "q", CurrentPeriod(), 3); o.ok || o.reason != "not_configured" {
		t.Fatalf("nil index outcome = %+v", o)
	}
	index := testIndex(t)
	if o := searchIndexMemory(context.Background(), index, decide.Reranker{}, "q", CurrentPeriod(), 3); o.ok || o.reason != "no_results" {
		t.Fatalf("empty index outcome = %+v", o)
	}
}

func TestIngestFetchedPage(t *testing.T) {
	index := testIndex(t)
	ingestFetchedPage(t.Context(), index, FetchURLResult{
		URL:    "https://docs.example/widget-frobnicator",
		Status: 200,
		Title:  "Widget frobnicator docs",
		Text:   strings.Repeat("widget frobnicator reference material ", 30),
	})
	// Non-2xx never ingests.
	ingestFetchedPage(t.Context(), index, FetchURLResult{URL: "https://docs.example/error", Status: 500, Title: "widget error"})
	index.Flush()
	docs, err := index.Search(context.Background(), "widget frobnicator docs", 5)
	testutil.FailErr(t, "index search", err)
	if len(docs) != 1 || docs[0].URL != "https://docs.example/widget-frobnicator" {
		t.Fatalf("docs = %+v want the fetched page only", docs)
	}
	if len(docs[0].Description) > fetchDescriptionLen {
		t.Fatalf("description = %d chars want clipped", len(docs[0].Description))
	}
}
