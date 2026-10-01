package webresearch

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestDirectLivePublicIndexFetch hits real public doc sites with provider seeds.
// Run with LYCAON_LIVE_DIRECT_FETCH=1:
// ./task test:digest -- ./internal/webresearch -run LivePublic -count=1 -timeout=3m
// Skipped under -short (CI / check-fast).
func TestDirectLivePublicIndexFetch(t *testing.T) {
	skipUnlessLiveDirectFetch(t)

	resetDirectState(2)

	type siteExpect struct {
		base       string
		wantSubstr string
	}

	sites := []siteExpect{
		{base: "https://docs.alpinelinux.org", wantSubstr: "alpine"},
		{base: "https://www.talos.dev", wantSubstr: "talos"},
		{base: "https://www.flatcar.org/docs", wantSubstr: "flatcar"},
	}

	d := discovererSeedingURLs(t,
		"https://docs.alpinelinux.org",
		"https://www.talos.dev",
		"https://www.flatcar.org/docs",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	hits, err := d.Search(ctx, DirectRequest{Query: "Alpine Talos Flatcar container linux immutable OS install", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected live hits")
	}
	t.Logf("live hits (%d):", len(hits))
	for _, h := range hits {
		t.Logf("  %s — %s", h.Title, h.URL)
	}

	found := map[string]bool{}
	for _, h := range hits {
		combined := strings.ToLower(h.Title + " " + h.URL)
		for _, s := range sites {
			if strings.Contains(combined, s.wantSubstr) {
				found[s.base] = true
			}
		}
	}
	for _, s := range sites {
		if !found[s.base] {
			t.Logf("warning: no hit matched seed %s (may need synthesis for sparse indexes)", s.base)
		}
	}
}

func TestDirectLiveSparseWithSynthesis(t *testing.T) {
	skipUnlessLiveDirectFetch(t)

	resetDirectState(1)

	// sqlite.org publishes no sitemap or llms.txt — seed the known WAL page
	// directly via the provider channel.
	base := "https://www.sqlite.org"
	synthURL := base + "/wal.html"

	d := providerBackedDiscoverer(t, "hn", "SQLite WAL write-ahead logging", synthURL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	hits, err := d.Search(ctx, DirectRequest{Query: "sqlite WAL write-ahead logging concurrent writers", MaxResults: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].URL != synthURL {
		t.Fatalf("url = %q want %q", hits[0].URL, synthURL)
	}
}

func TestDirectLiveParallelQueries(t *testing.T) {
	skipUnlessLiveDirectFetch(t)

	resetDirectState(2)

	queries := []string{
		"Alpine Linux apk package manager",
		"Talos Kubernetes immutable OS",
		"Flatcar Container Linux ignition",
	}

	seeds := []string{
		"https://docs.alpinelinux.org",
		"https://www.talos.dev",
		"https://www.flatcar.org/docs",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	type result struct {
		idx int
		out providerOutcome
	}
	ch := make(chan result, len(queries))
	for i, q := range queries {
		go func() {
			d := discovererSeedingURLs(t, seeds...)
			out := searchDirect(ctx, d, q, CurrentPeriod(), 3)
			ch <- result{idx: i, out: out}
		}()
	}

	ok := 0
	for range queries {
		r := <-ch
		if r.out.ok {
			ok++
			t.Logf("query %q: %d hits", queries[r.idx], len(r.out.hits))
			for _, h := range r.out.hits {
				t.Logf("  %s — %s", h.Title, h.URL)
			}
			continue
		}
		t.Logf("query %q failed: %s — %s", queries[r.idx], r.out.reason, r.out.detail)
	}
	if ok < len(queries) {
		t.Fatalf("only %d/%d parallel queries succeeded", ok, len(queries))
	}
}

func TestDirectLiveTalosQueryIsolated(t *testing.T) {
	skipUnlessLiveDirectFetch(t)
	resetDirectState(1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d := discovererSeedingURLs(t,
		"https://docs.alpinelinux.org",
		"https://www.talos.dev",
		"https://www.flatcar.org/docs",
	)
	out := searchDirect(ctx, d, "Talos Kubernetes immutable OS", CurrentPeriod(), 3)
	if !out.ok {
		t.Fatalf("outcome = %+v", out)
	}
}
