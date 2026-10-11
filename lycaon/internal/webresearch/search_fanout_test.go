package webresearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	cat, err := LoadCatalog()
	testutil.FailErr(t, "LoadCatalog", err)
	return cat
}

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	reg := NewRegistry(testCatalog(t))
	testutil.FailErr(t, "RegisterCatalogProviders", RegisterCatalogProviders(t.Context(), reg))
	return reg
}

func assertStringSetEqual(t *testing.T, label string, want, got []string) {
	t.Helper()
	w := append([]string(nil), want...)
	g := append([]string(nil), got...)
	sort.Strings(w)
	sort.Strings(g)
	if len(w) != len(g) {
		t.Fatalf("%s: want %v got %v", label, w, g)
	}
	for i := range w {
		if w[i] != g[i] {
			t.Fatalf("%s: want %v got %v", label, w, g)
		}
	}
}

func braveStubRegistry(t *testing.T, hitURL string) *Registry {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"web": map[string]any{
				"results": []map[string]string{{
					"url": hitURL, "title": "Brave hit", "description": "snippet",
				}},
			},
		})
	}))
	t.Cleanup(srv.Close)
	spec := braveRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?q=" + url.QueryEscape(query), nil
	}
	reg := NewRegistry(testCatalog(t))
	reg.Register(NewRESTSearchProvider(spec, KindKeyed))
	injectProviderHTTPClient(t, srv.Client())
	return reg
}

func TestSearchDirectRouterUnavailable(t *testing.T) {
	result := Search(context.Background(), SearchOptions{
		Query:    "router unavailable probe",
		Settings: Settings{MaxResultsDefault: 10, EnabledProviders: []string{"direct"}},
	})
	if !result.OK {
		t.Fatalf("expected soft ok, got %+v", result)
	}
	if len(result.Results) != 0 {
		t.Fatalf("results = %+v want empty", result.Results)
	}
	if len(result.ProvidersSkipped) != 1 || result.ProvidersSkipped[0].Provider != "direct" {
		t.Fatalf("skipped = %+v want direct router_unavailable", result.ProvidersSkipped)
	}
}

func TestSearchExplicitProviderBypassesEnabledSet(t *testing.T) {
	d := &FakeDirectDiscoverer{Hits: []WebHit{{URL: "https://example.com/direct", Provider: "direct"}}}
	result := Search(context.Background(), SearchOptions{
		Query:      "explicit direct probe",
		Provider:   "direct",
		Settings:   Settings{EnabledProviders: []string{}},
		Discoverer: d,
		Registry:   testRegistry(t),
	})
	if d.Calls != 1 {
		t.Fatalf("direct discoverer calls = %d want 1 for explicit direct", d.Calls)
	}
	if len(result.Results) != 1 || result.Results[0].Provider != "direct" {
		t.Fatalf("result = %+v", result)
	}
}

func TestSearchExplicitUnconfiguredProviderSkips(t *testing.T) {
	result := Search(context.Background(), SearchOptions{
		Query:    "explicit brave probe",
		Provider: "brave",
		Settings: Settings{EnabledProviders: []string{"direct"}},
		Registry: testRegistry(t),
	})
	if !result.OK || len(result.Results) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.ProvidersSkipped) != 1 || result.ProvidersSkipped[0].Reason != "auth_missing" {
		t.Fatalf("skipped = %+v", result.ProvidersSkipped)
	}
}

func TestSearchFanOutDirectOnlyIgnoresDisabledProviders(t *testing.T) {
	d := &FakeDirectDiscoverer{Hits: []WebHit{{URL: "https://example.com/direct", Provider: "direct"}}}
	result := Search(context.Background(), SearchOptions{
		Query: "direct only probe",
		Settings: Settings{
			EnabledProviders: []string{"direct"},
			Keys:             map[string]string{"brave": "key"},
		},
		Discoverer: d,
		Registry:   testRegistry(t),
	})
	if d.Calls != 1 {
		t.Fatalf("direct discoverer calls = %d want 1", d.Calls)
	}
	if len(result.Results) != 1 || result.Results[0].Provider != "direct" {
		t.Fatalf("result = %+v", result)
	}
}

func TestSearchNoProvidersEnabledSoftSkip(t *testing.T) {
	d := &FakeDirectDiscoverer{Hits: []WebHit{{URL: "https://example.com"}}}
	result := Search(context.Background(), SearchOptions{
		Query:      "no providers probe",
		Settings:   Settings{EnabledProviders: []string{}},
		Discoverer: d,
		Registry:   testRegistry(t),
	})
	if d.Calls != 0 {
		t.Fatalf("direct discoverer calls = %d want 0", d.Calls)
	}
	if !result.OK || len(result.Results) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.ProvidersSkipped) != 1 || result.ProvidersSkipped[0].Reason != "no_providers_enabled" {
		t.Fatalf("skipped = %+v", result.ProvidersSkipped)
	}
}

func TestSearchFanOutProviderOnlyWithoutDirect(t *testing.T) {
	reg := braveStubRegistry(t, "https://example.com/brave")
	d := &FakeDirectDiscoverer{Hits: []WebHit{{URL: "https://example.com/direct", Provider: "direct"}}}
	result := Search(context.Background(), SearchOptions{
		Query: "brave only probe",
		Settings: Settings{
			Keys:             map[string]string{"brave": "brave-key"},
			EnabledProviders: []string{"brave"},
		},
		Discoverer: d,
		Registry:   reg,
	})
	if d.Calls != 0 {
		t.Fatalf("direct discoverer calls = %d want 0 when direct is off", d.Calls)
	}
	if len(result.Results) != 1 || result.Results[0].Provider != "brave" {
		t.Fatalf("result = %+v", result)
	}
}

func TestSearchFanOutDirectBesideProvider(t *testing.T) {
	reg := braveStubRegistry(t, "https://example.com/brave")
	d := &FakeDirectDiscoverer{Hits: []WebHit{{URL: "https://example.com/direct", Title: "Direct hit", Provider: "direct"}}}
	result := Search(context.Background(), SearchOptions{
		Query: "mixed fanout probe",
		Settings: Settings{
			Keys:             map[string]string{"brave": "brave-key"},
			EnabledProviders: []string{"direct", "brave"},
		},
		Discoverer: d,
		Registry:   reg,
	})
	if d.Calls != 1 {
		t.Fatalf("direct discoverer calls = %d want 1 in mixed fan-out", d.Calls)
	}
	if !result.OK || len(result.Results) != 2 {
		t.Fatalf("result = %+v want fused direct + brave hits", result)
	}
	providers := []string{result.Results[0].Provider, result.Results[1].Provider}
	assertStringSetEqual(t, "fused providers", []string{"brave", "direct"}, providers)
}

func TestSearchFanOutSkipsUnconfiguredEnabledProvider(t *testing.T) {
	d := &FakeDirectDiscoverer{Hits: []WebHit{{URL: "https://example.com/direct", Provider: "direct"}}}
	result := Search(context.Background(), SearchOptions{
		Query: "skip unconfigured probe",
		Settings: Settings{
			EnabledProviders: []string{"direct", "brave"},
		},
		Discoverer: d,
		Registry:   testRegistry(t),
	})
	if !result.OK || len(result.Results) != 1 || result.Results[0].Provider != "direct" {
		t.Fatalf("result = %+v", result)
	}
	found := false
	for _, sk := range result.ProvidersSkipped {
		if sk.Provider == "brave" && sk.Reason == "not_configured" {
			found = true
		}
	}
	if !found {
		t.Fatalf("skipped = %+v want brave not_configured", result.ProvidersSkipped)
	}
}

func TestConfigStorePrefsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-research-config.yaml")
	store := NewConfigStoreAt(path)
	testutil.FailErr(t, "ApplyPrefs", store.ApplyPrefs(nil, nil, nil, []string{"direct", "brave", "serper"}))
	reloaded := NewConfigStoreAt(path)
	providers, explicit := reloaded.ProviderSelection()
	assertStringSetEqual(t, "enabled", []string{"direct", "brave", "serper"}, providers)
	if !explicit {
		t.Fatal("provider selection is not explicit")
	}
}

func TestConfigStorePrefsPreservedAcrossPartialUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-research-config.yaml")
	store := NewConfigStoreAt(path)
	testutil.FailErr(t, "ApplyPrefs enabled", store.ApplyPrefs(nil, nil, nil, []string{"brave"}))
	warming, guess := true, false
	testutil.FailErr(t, "ApplyPrefs warming", store.ApplyPrefs(&warming, &guess, nil, nil))
	reloaded := NewConfigStoreAt(path)
	providers, explicit := reloaded.ProviderSelection()
	assertStringSetEqual(t, "enabled preserved", []string{"brave"}, providers)
	if !explicit {
		t.Fatal("provider selection is not explicit")
	}
	if reloaded.Warming() != WarmingCrawlOnly {
		t.Fatalf("warming = %q want crawl_only", reloaded.Warming())
	}
}

func TestDefaultSettingsReadsPersistedPrefs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-research-config.yaml")
	cfg := NewConfigStoreAt(path)
	testutil.FailErr(t, "ApplyPrefs", cfg.ApplyPrefs(nil, nil, nil, []string{"tavily"}))
	cat := testCatalog(t)
	s := DefaultSettings(nil, cfg, cat)
	assertStringSetEqual(t, "enabled", []string{"tavily"}, s.EnabledProviders)
}

func TestFilterKnownProviderIDs(t *testing.T) {
	cat := testCatalog(t)
	got := FilterKnownProviderIDs(cat, []string{"brave", "direct", "unknown", "serper", "direct"})
	assertStringSetEqual(t, "filtered", []string{"brave", "direct", "serper"}, got)
}
