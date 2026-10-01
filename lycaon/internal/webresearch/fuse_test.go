package webresearch

import "testing"

func TestFuseHitsCrossProviderAgreementBoosts(t *testing.T) {
	merged := fuseHits([]providerOutcome{
		{ok: true, hits: []WebHit{
			{URL: "https://example.com/a", Provider: "brave"},
			{URL: "https://example.com/b", Provider: "brave"},
		}},
		{ok: true, hits: []WebHit{
			{URL: "https://example.com/b", Provider: "marginalia"},
			{URL: "https://example.com/c", Provider: "marginalia"},
		}},
	}, 10)
	if len(merged) != 3 {
		t.Fatalf("merged len = %d want 3", len(merged))
	}
	if merged[0].URL != "https://example.com/b" {
		t.Fatalf("top hit = %q want the URL both providers returned", merged[0].URL)
	}
}

func TestFuseHitsTieKeepsProviderOrder(t *testing.T) {
	merged := fuseHits([]providerOutcome{
		{ok: true, hits: []WebHit{{URL: "https://example.com/a", Provider: "brave"}}},
		{ok: true, hits: []WebHit{{URL: "https://example.com/b", Provider: "marginalia"}}},
	}, 10)
	if len(merged) != 2 {
		t.Fatalf("merged len = %d want 2", len(merged))
	}
	if merged[0].Provider != "brave" || merged[1].Provider != "marginalia" {
		t.Fatalf("tie order = %q, %q want brave, marginalia", merged[0].Provider, merged[1].Provider)
	}
}

func TestFuseHitsDedupesNormalizedURLs(t *testing.T) {
	merged := fuseHits([]providerOutcome{
		{ok: true, hits: []WebHit{{URL: "http://www.example.com/a/", Title: "first seen"}}},
		{ok: true, hits: []WebHit{{URL: "https://example.com/a?utm_source=x", Title: "dup"}}},
	}, 10)
	if len(merged) != 1 {
		t.Fatalf("merged len = %d want 1", len(merged))
	}
	if merged[0].Title != "first seen" {
		t.Fatalf("kept hit = %q want the first-seen hit", merged[0].Title)
	}
	if merged[0].URL != "http://www.example.com/a/" {
		t.Fatalf("kept URL = %q want original, not canonical form", merged[0].URL)
	}
}

func TestFuseHitsTruncatesAndSkipsFailedOutcomes(t *testing.T) {
	merged := fuseHits([]providerOutcome{
		{ok: false, hits: []WebHit{{URL: "https://example.com/failed"}}},
		{ok: true, hits: []WebHit{
			{URL: "https://example.com/a"},
			{URL: "https://example.com/b"},
			{URL: "https://example.com/c"},
		}},
	}, 2)
	if len(merged) != 2 {
		t.Fatalf("merged len = %d want 2", len(merged))
	}
	for _, hit := range merged {
		if hit.URL == "https://example.com/failed" {
			t.Fatal("hit from failed outcome leaked into results")
		}
	}
	if merged[0].URL != "https://example.com/a" || merged[1].URL != "https://example.com/b" {
		t.Fatalf("merged = %+v want single-provider rank order preserved", merged)
	}
}

func TestCanonicalURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"scheme and www collapse", "http://www.Example.com/A/", "https://example.com/A"},
		{"default port drops", "https://example.com:443/a", "https://example.com/a"},
		{"tracking params drop", "https://example.com/a?utm_source=x&gclid=1&page=2", "https://example.com/a?page=2"},
		{"fragment drops", "https://example.com/a#section", "https://example.com/a"},
		{"query order stable", "https://example.com/a?b=2&a=1", "https://example.com/a?a=1&b=2"},
		{"unparseable passes through", "://not-a-url", "://not-a-url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canonicalURL(tc.in); got != tc.want {
				t.Fatalf("canonicalURL(%q) = %q want %q", tc.in, got, tc.want)
			}
		})
	}
}
