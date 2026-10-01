package webresearch

import (
	"context"
	"strings"
	"testing"
	"time"
)

// liveQueryCase simulates a good seed-LLM response for a fresh-topic search.
type liveQueryCase struct {
	name      string
	query     string
	seedJSON  string
	minHits   int
	minHosts  int
	mustMatch func(h WebHit) bool
}

// TestDirectLiveSearchQuality runs live searches with fixed seed plans.
// Run with LYCAON_LIVE_DIRECT_FETCH=1:
// ./task test:digest -- ./internal/webresearch -run LiveSearchQuality -count=1 -timeout=5m
func TestDirectLiveSearchQuality(t *testing.T) {
	skipUnlessLiveDirectFetch(t)

	cases := []liveQueryCase{
		{
			name:     "fresh_reviews_with_leads",
			query:    "steam machine reviews",
			seedJSON: `{"seeds":["https://www.theverge.com","https://www.polygon.com","https://arstechnica.com","https://www.pcgamer.com","https://www.ign.com","https://www.eurogamer.net"],"fresh":true,"expand":["Valve Steam Machine","Steam hardware review"],"leads":[{"title":"The Steam Machine review","host":"www.theverge.com"},{"title":"Steam Machine review benchmarks","host":"gamersnexus.net"},{"title":"Steam Machine hardware review","host":"www.digitalfoundry.net"}]}`,
			minHits:  4,
			minHosts: 2,
			mustMatch: func(h WebHit) bool {
				s := strings.ToLower(h.Title + " " + h.URL)
				return strings.Contains(s, "steam") && strings.Contains(s, "machine")
			},
		},
		{
			name:     "fresh_topic_sitemap_only",
			query:    "steam machine reviews",
			seedJSON: `{"seeds":["https://www.theverge.com","https://www.polygon.com","https://arstechnica.com","https://www.pcgamer.com"],"fresh":true,"expand":["Valve Steam Machine","Steam hardware"]}`,
			minHits:  3,
			minHosts: 1,
			mustMatch: func(h WebHit) bool {
				return strings.Contains(strings.ToLower(h.Title+" "+h.URL), "steam")
			},
		},
		{
			name:     "stable_docs",
			query:    "Talos Kubernetes immutable OS install",
			seedJSON: `{"seeds":["https://www.talos.dev","https://docs.alpinelinux.org","https://www.flatcar.org/docs"],"fresh":false,"expand":["Talos Linux","Flatcar Container Linux"]}`,
			minHits:  2,
			minHosts: 1,
			mustMatch: func(h WebHit) bool {
				s := strings.ToLower(h.Title + " " + h.URL)
				return strings.Contains(s, "talos") || strings.Contains(s, "flatcar") || strings.Contains(s, "alpine")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetDirectState(2)
			plan, err := parseSeedsJSON(tc.seedJSON)
			if err != nil {
				t.Fatalf("parseSeedsJSON: %v", err)
			}
			d := discovererSeedingURLs(t, plan.seeds...)
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			hits, err := d.Search(ctx, DirectRequest{Query: tc.query, MaxResults: 5})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(hits) < tc.minHits {
				t.Fatalf("hits = %d want >= %d", len(hits), tc.minHits)
			}
			hosts := map[string]struct{}{}
			matched := 0
			for _, h := range hits {
				t.Logf("  %s — %s", h.Title, h.URL)
				hosts[strings.ToLower(urlHost(h.URL))] = struct{}{}
				if tc.mustMatch(h) {
					matched++
				}
			}
			if len(hosts) < tc.minHosts {
				t.Fatalf("host breadth = %d want >= %d", len(hosts), tc.minHosts)
			}
			if matched == 0 {
				t.Fatal("no hits matched relevance predicate")
			}
			t.Logf("ok: %d hits, %d hosts, %d relevant", len(hits), len(hosts), matched)
		})
	}
}

func TestDirectLiveIndexFindsReviewFromHostLeads(t *testing.T) {
	skipUnlessLiveDirectFetch(t)
	resetDirectState(1)
	seedJSON := `{"seeds":["https://www.theverge.com"],"fresh":true,"expand":["Steam Machine review"],"leads":[{"title":"The Steam Machine review","host":"www.theverge.com"}]}`
	plan, err := parseSeedsJSON(seedJSON)
	if err != nil {
		t.Fatalf("parseSeedsJSON: %v", err)
	}
	d := discovererSeedingURLs(t, plan.seeds...)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	hits, err := d.Search(ctx, DirectRequest{Query: "Steam Machine 2026 hardware console review", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, h := range hits {
		t.Logf("  %s — %s", h.Title, h.URL)
		if strings.Contains(strings.ToLower(h.URL), "steam-machine") {
			found = true
		}
	}
	if !found {
		t.Fatalf("index crawl missed steam machine review URL: %+v", hits)
	}
}
