package webresearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBackoffQueryVariants(t *testing.T) {
	got := backoffQueryVariants("Steam Machine 2025 mini PC review quiet living room gaming", 3)
	want := []string{
		"Steam Machine 2025 mini PC review quiet living room gaming",
		"Steam Machine 2025 mini PC review quiet living room",
		"Steam Machine 2025 mini PC review quiet living",
		"Steam Machine 2025 mini PC review quiet",
		"Steam Machine 2025 mini PC review",
		"Steam Machine 2025 mini PC",
		"Steam Machine 2025 mini",
		"Steam Machine 2025",
	}
	if len(got) != len(want) {
		t.Fatalf("variants = %d want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("variants[%d] = %q want %q", i, got[i], want[i])
		}
	}
}

func TestBackoffQueryVariantsShorterThanFloor(t *testing.T) {
	got := backoffQueryVariants("Steam Machine", 3)
	if len(got) != 1 || got[0] != "Steam Machine" {
		t.Fatalf("variants = %v want the query itself", got)
	}
}

func TestBackoffQueryVariantsEmpty(t *testing.T) {
	if got := backoffQueryVariants("   ", 3); got != nil {
		t.Fatalf("variants = %v want nil", got)
	}
}

// backoffServer returns hits for one query and counts calls.
func backoffServer(t *testing.T, hitQuery string, calls *int) string {
	t.Helper()
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if r.URL.Query().Get("s") == hitQuery {
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"url":     "https://example.com/steam-machine",
				"title":   []map[string]string{{"value": "Steam Machine"}},
				"extract": []map[string]string{{"value": "living room PC"}},
			}})
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})
	return srv.URL
}

// Catalog registration applies query backoff to provider adapters.
func TestCatalogQueryBackoffRetriesShorterQueries(t *testing.T) {
	longQuery := "Steam Machine 2025 mini PC review quiet living room gaming"
	shortQuery := "Steam Machine 2025 mini PC review quiet"
	calls := 0
	endpoint := backoffServer(t, shortQuery, &calls)
	provider := testRegistry(t).Get("mwmbl")
	out := provider.Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"mwmbl": {"endpoint": endpoint}},
		PerProviderTimeoutSec: 5,
	}, longQuery, 5)
	if !out.ok || len(out.hits) != 1 || out.hits[0].URL != "https://example.com/steam-machine" {
		t.Fatalf("out = %+v", out)
	}
	if calls != 4 {
		t.Fatalf("calls = %d want 4 (full query through short hit)", calls)
	}
}

func TestCatalogQueryBackoffMarginaliaLongQuery(t *testing.T) {
	longQuery := "best web based chess libraries 2024 2025 JavaScript React Vue HTML5"
	shortQuery := "best web based chess libraries 2024 2025 JavaScript React Vue"
	calls := 0
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("query") == shortQuery {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]string{{
					"url": "https://example.com/chess-lib", "title": "Chess libs", "description": "snippet",
				}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	})
	spec := marginaliaRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		u, err := url.Parse(srv.URL)
		if err != nil {
			return "", err
		}
		q := u.Query()
		q.Set("query", query)
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	reg := NewRegistry(testCatalog(t))
	reg.registerREST(spec, KindKeyed)
	out := reg.Get("marginalia").Search(context.Background(), Settings{
		Keys:                  map[string]string{"marginalia": "secret"},
		PerProviderTimeoutSec: 5,
	}, longQuery, 5)
	if !out.ok || len(out.hits) != 1 || out.hits[0].URL != "https://example.com/chess-lib" {
		t.Fatalf("out = %+v", out)
	}
	if calls != 2 {
		t.Fatalf("calls = %d want 2 (full query then shortened hit)", calls)
	}
}

func TestCatalogQueryBackoffShortQuerySearchesOnce(t *testing.T) {
	calls := 0
	endpoint := backoffServer(t, "Steam Machine", &calls)
	provider := testRegistry(t).Get("mwmbl")
	out := provider.Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"mwmbl": {"endpoint": endpoint}},
		PerProviderTimeoutSec: 5,
	}, "Steam Machine", 5)
	if !out.ok || len(out.hits) != 1 {
		t.Fatalf("out = %+v", out)
	}
	if calls != 1 {
		t.Fatalf("calls = %d want 1", calls)
	}
}

func TestCatalogQueryBackoffReservesQuotaPerOutboundRequest(t *testing.T) {
	stageCatalogYAML(t, `
providers:
  - id: mwmbl
    kind: keyless
    label: Mwmbl
    default_endpoint: https://mwmbl.org
    test_query: test
    pacing:
      daily_cap: 2
`)
	cat, err := LoadCatalog()
	testutil.FailErr(t, "load catalog", err)
	quota := &memQuotaStore{}
	reg := NewRegistry(cat)
	reg.AttachQuotaStore(quota)
	testutil.FailErr(t, "RegisterCatalogProviders", RegisterCatalogProviders(reg))
	calls := 0
	endpoint := backoffServer(t, "never matches", &calls)

	out := reg.Get("mwmbl").Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"mwmbl": {"endpoint": endpoint}},
		PerProviderTimeoutSec: 5,
	}, "one two three four five", 5)
	if out.reason != "paced" {
		t.Fatalf("out = %+v want paced after two admitted requests", out)
	}
	if calls != 2 {
		t.Fatalf("HTTP calls = %d want quota cap 2", calls)
	}
	count, err := quota.ProviderQuotaCount(context.Background(), "mwmbl", ProviderQuotaDayUTC(time.Now()))
	testutil.FailErr(t, "read quota count", err)
	if count != 2 {
		t.Fatalf("quota count = %d want 2", count)
	}
}
