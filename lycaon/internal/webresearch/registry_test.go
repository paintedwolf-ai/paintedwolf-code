package webresearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRegistryBraveSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Subscription-Token"); got != "secret" {
			t.Fatalf("token = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"web": map[string]any{
				"results": []map[string]string{{
					"url":         "https://example.com/hit",
					"title":       "Hit",
					"description": "snippet",
				}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	orig := braveSearchURL
	override := srv.URL
	// braveRESTSpec uses const URL — test via custom spec pointed at srv
	spec := braveRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return override + "?q=" + query, nil
	}
	reg := NewRegistry(testCatalog(t))
	reg.Register(NewRESTSearchProvider(spec, KindKeyed))

	injectProviderHTTPClient(t, srv.Client())
	_ = orig

	out := reg.Get("brave").Search(context.Background(), Settings{
		Keys:                  map[string]string{"brave": "secret"},
		PerProviderTimeoutSec: 5,
	}, "react", 5)
	if !out.ok || len(out.hits) != 1 || out.hits[0].URL != "https://example.com/hit" {
		t.Fatalf("out = %+v", out)
	}
}

func TestDefaultSettingsResolvesCatalogKeys(t *testing.T) {
	dir := t.TempDir()
	cat := testCatalog(t)
	store := NewCredentialStoreAt(dir+"/credential-vault.age", cat)
	testutil.FailErr(t, "Set brave", store.Set("brave-search", "stored-brave"))
	s := DefaultSettings(store, nil, cat)
	if s.Keys["brave"] != "stored-brave" {
		t.Fatalf("keys = %+v", s.Keys)
	}
}

func TestRegistryListsProviders(t *testing.T) {
	reg := testRegistry(t)
	if len(reg.IDs()) != len(testCatalog(t).Entries()) {
		t.Fatalf("ids = %v want %d", reg.IDs(), len(testCatalog(t).Entries()))
	}
	if reg.Catalog() == nil || len(reg.Catalog().Entries()) == 0 {
		t.Fatal("expected catalog on registry")
	}
}
