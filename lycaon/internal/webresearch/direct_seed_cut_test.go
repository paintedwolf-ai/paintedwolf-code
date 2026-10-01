package webresearch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func seedCutTestSite(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			fmt.Fprint(w, "User-agent: *\nAllow: /\n")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Widget frobnicator review</title>
			<meta name="description" content="widget frobnicator review guide"></head>
			<body><h1>Widget frobnicator review</h1></body></html>`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Slow provider probes are canceled once another channel supplies crawl work.
func TestProviderSeedChannelsCutWhenFastContributorLands(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)

	prevGrace := providerSeedGraceTimeout
	providerSeedGraceTimeout = 200 * time.Millisecond
	t.Cleanup(func() { providerSeedGraceTimeout = prevGrace })

	srv := seedCutTestSite(t)
	stageCatalogYAML(t, `
providers:
  - id: fast_seed
    kind: keyless
    label: Fast seed
    test_query: t
    default_endpoint: https://example.com
    roles: [results, seeds]
    default_enabled: true
  - id: slow_seed
    kind: keyless
    label: Slow seed
    test_query: t
    default_endpoint: https://example.com
    roles: [results, seeds]
    default_enabled: true
`)
	cat, err := LoadCatalog()
	testutil.FailErr(t, "LoadCatalog", err)
	reg := NewRegistry(cat)
	reg.Register(&stubSeedProvider{
		id: "fast_seed",
		hits: []WebHit{{
			Title:    "Widget frobnicator review",
			URL:      srv.URL + "/guide",
			Snippet:  "widget frobnicator review",
			Provider: "fast_seed",
		}},
	})
	reg.Register(&slowSleepStubProvider{id: "slow_seed", delay: 30 * time.Second})

	d := &directDiscoverer{
		summarizer: &failIfCalledSummarizer{},
		registry:   reg,
		settings:   Settings{},
		seedOrigin: searchOriginUser,
	}
	stats := newSearchStats("t-provider-seed-cut")
	ctx := withSearchStats(context.Background(), stats)
	start := time.Now()
	hits, err := d.Search(ctx, DirectRequest{Query: "widget frobnicator review", MaxResults: 1})
	testutil.FailErr(t, "search", err)
	if len(hits) != 1 || hits[0].Provider != "fast_seed" {
		t.Fatalf("hits = %+v want the fast provider seed hit", hits)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("search took %v — slow provider seed was not cut", elapsed)
	}
	if cut, _ := stats.providerSeedCut.Load().(string); cut == "" {
		t.Fatal("expected provider_seed_cut after fast seed contribution")
	}
}
