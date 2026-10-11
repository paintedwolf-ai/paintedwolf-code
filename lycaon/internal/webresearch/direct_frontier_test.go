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

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/webindex"
)

// TestDirectIndexMemoryAnswersWhenProviderMissesHost: a page verified in an
// earlier search is findable when provider seeds only name an unrelated host —
// memory widens discovery past the live seed channels.
func TestDirectIndexMemoryAnswersWhenProviderMissesHost(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Widget frobnicator review</title>
			<meta name="description" content="widget frobnicator review with benchmarks"></head>
			<body><h1>Widget frobnicator review</h1></body></html>`)
	}))
	t.Cleanup(target.Close)

	store, err := webindex.Open(t.Context(), filepath.Join(t.TempDir(), "web-index.db"))
	testutil.FailErr(t, "open index", err)
	t.Cleanup(func() { _ = store.Close() })
	store.QueuePage(t.Context(), webindex.Page{
		URL:      target.URL + "/",
		Title:    "Widget frobnicator review",
		Verified: true,
	})
	store.Flush()

	d := &directDiscoverer{
		summarizer: &failIfCalledSummarizer{},
		index:      store,
	}
	hits, err := d.Search(context.Background(), DirectRequest{Query: "widget frobnicator review", MaxResults: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, h := range hits {
		if strings.HasPrefix(h.URL, target.URL) {
			found = true
		}
	}
	if !found {
		t.Fatalf("hits = %+v want index-memory host", hits)
	}
}

// TestDirectMemoryAnswersWithoutModel verifies phase 1 returns from index
// memory without invoking the Summarizer when strong hits meet the budget.
func TestDirectMemoryAnswersWithoutModel(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Widget frobnicator review</title>
			<meta name="description" content="widget frobnicator review with benchmarks"></head>
			<body><h1>Widget frobnicator review</h1></body></html>`)
	}))
	t.Cleanup(target.Close)

	store, err := webindex.Open(t.Context(), filepath.Join(t.TempDir(), "web-index.db"))
	testutil.FailErr(t, "open index", err)
	t.Cleanup(func() { _ = store.Close() })
	store.QueuePage(t.Context(), webindex.Page{
		URL:      target.URL + "/",
		Title:    "Widget frobnicator review",
		Verified: true,
	})
	store.Flush()

	d := &directDiscoverer{
		summarizer: &failIfCalledSummarizer{},
		index:      store,
	}
	hits, err := d.Search(context.Background(), DirectRequest{Query: "widget frobnicator review", MaxResults: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || !strings.HasPrefix(hits[0].URL, target.URL) {
		t.Fatalf("hits = %+v want single memory hit", hits)
	}
}

// TestDirectSearchIngestsIntoIndex verifies the write path: hits and anchor
// observations from a search land in the persistent index.
func TestDirectSearchIngestsIntoIndex(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/llms.txt" {
			fmt.Fprintf(w, "- [Guide](%s/guide.md): widget frobnicator guide\n", srvURL(r))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	store, err := webindex.Open(t.Context(), filepath.Join(t.TempDir(), "web-index.db"))
	testutil.FailErr(t, "open index", err)
	t.Cleanup(func() { _ = store.Close() })

	d := discovererSeedingURLsWithIndex(t, store, srv.URL)
	hits, err := d.Search(context.Background(), DirectRequest{Query: "widget frobnicator guide", MaxResults: 2})
	if err != nil || len(hits) == 0 {
		t.Fatalf("Search: hits=%v err=%v", hits, err)
	}
	store.Flush()
	docs, err := store.Search(context.Background(), "widget frobnicator guide", 5)
	testutil.FailErr(t, "index search", err)
	if len(docs) == 0 || !docs[0].Verified {
		t.Fatalf("docs = %+v want verified ingested hit", docs)
	}
}

// TestDirectDeadIndexMemoryURLEvicted closes the verification feedback loop:
// a remembered URL that probes dead is deleted from the index so it never
// wastes another probe.
func TestDirectDeadIndexMemoryURLEvicted(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/llms.txt" {
			fmt.Fprintf(w, "- [Guide](%s/guide.md): widget frobnicator guide\n", srvURL(r))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(live.Close)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(dead.Close)

	store, err := webindex.Open(t.Context(), filepath.Join(t.TempDir(), "web-index.db"))
	testutil.FailErr(t, "open index", err)
	t.Cleanup(func() { _ = store.Close() })
	deadURL := dead.URL + "/gone/widget-frobnicator-review"
	store.QueuePage(t.Context(), webindex.Page{URL: deadURL, Title: "widget frobnicator review", Verified: true})
	store.Flush()

	d := discovererSeedingURLsWithIndex(t, store, live.URL)
	if _, err := d.Search(context.Background(), DirectRequest{Query: "widget frobnicator guide", MaxResults: 3}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	store.Flush()
	docs, err := store.Search(context.Background(), "widget frobnicator review", 5)
	testutil.FailErr(t, "index search", err)
	for _, doc := range docs {
		if doc.URL == deadURL {
			t.Fatalf("docs = %+v want dead URL evicted after failed probe", docs)
		}
	}
}

// TestDirectMemoryAnswersFromWarmIndex: a warm index fills the hit budget
// without any provider seed or Summarizer.
func TestDirectMemoryAnswersFromWarmIndex(t *testing.T) {
	testutil.SkipIfShort(t, "memory-only search with multi-second budget")
	allowLoopbackFetch(t)
	resetDirectState(1)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Widget frobnicator review</title>
			<meta name="description" content="widget frobnicator review with benchmarks"></head>
			<body><h1>Widget frobnicator review</h1></body></html>`)
	}))
	t.Cleanup(page.Close)

	store, err := webindex.Open(t.Context(), filepath.Join(t.TempDir(), "web-index.db"))
	testutil.FailErr(t, "open index", err)
	t.Cleanup(func() { _ = store.Close() })
	store.QueuePage(t.Context(), webindex.Page{URL: page.URL + "/", Title: "Widget frobnicator review", Verified: true})
	store.Flush()

	d := &directDiscoverer{
		summarizer: &failIfCalledSummarizer{},
		index:      store,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	hits, err := d.Search(ctx, DirectRequest{Query: "widget frobnicator review", MaxResults: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || !strings.HasPrefix(hits[0].URL, page.URL) {
		t.Fatalf("hits = %+v want memory-backed hit", hits)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("elapsed = %v want memory answer without waiting on network seed work", elapsed)
	}
}

// TestDirectStragglerHostDoesNotBlockResults: one responsive host fills the
// budget while another host's crawl hangs — no barrier waits for it.
func TestDirectStragglerHostDoesNotBlockResults(t *testing.T) {
	testutil.SkipIfShort(t, "straggler host crawl with multi-second budget")
	allowLoopbackFetch(t)
	resetDirectState(1)
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/llms.txt" {
			fmt.Fprintf(w, "- [Guide](%s/guide.md): widget frobnicator guide with benchmarks\n", srvURL(r))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Widget frobnicator guide</title>
			<meta name="description" content="widget frobnicator guide with benchmarks"></head>
			<body><h1>Widget frobnicator guide</h1></body></html>`)
	}))
	t.Cleanup(fast.Close)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(8 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(slow.Close)

	d := discovererSeedingURLs(t, fast.URL, slow.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	hits, err := d.Search(ctx, DirectRequest{Query: "widget frobnicator guide", MaxResults: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Fatalf("elapsed = %v: the hanging host must not gate the result", elapsed)
	}
}

func TestFrontierResidualURLsRanksUnprobedLeftovers(t *testing.T) {
	fr := newFrontier(2, maxHitsPerHost)
	fr.admit(
		indexCandidate{URL: "https://a.example/widget-frobnicator", Title: "widget frobnicator guide"},
		indexCandidate{URL: "https://b.example/widget", Title: "widget overview"},
		indexCandidate{URL: "https://c.example/unrelated", Title: "cooking recipes"},
	)
	scorer := newQueryScorer("widget frobnicator", nil, false, CurrentPeriod())
	got := fr.residualURLs(scorer, 2)
	if len(got) != 2 || got[0] != "https://a.example/widget-frobnicator" {
		t.Fatalf("residuals = %v want top query matches, zero-signal dropped", got)
	}
	// A probed candidate never comes back as residual.
	fr.probed[canonicalURL("https://a.example/widget-frobnicator")] = struct{}{}
	got = fr.residualURLs(scorer, 2)
	if len(got) != 1 || got[0] != "https://b.example/widget" {
		t.Fatalf("residuals = %v want probed excluded", got)
	}
	if urls := fr.residualURLs(scorer, 0); urls != nil {
		t.Fatalf("residuals = %v want nil at zero cap", urls)
	}
}
