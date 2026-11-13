package webresearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressgate"
)

// countingServer counts the requests one origin receives.
func countingServer(t *testing.T, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("body"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A search sends its query to the providers the host selected and nowhere
// else: not to an address written in the query, and not to a result it returns.
func TestSearchQueryReachesOnlySelectedProviders(t *testing.T) {
	var inQuery, inResult, atProvider atomic.Int32
	named := countingServer(t, &inQuery)
	result := countingServer(t, &inResult)

	var received atomic.Value
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atProvider.Add(1)
		received.Store(r.URL.Query().Get("q"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"web": map[string]any{"results": []map[string]string{{
				"url": result.URL + "/hit", "title": "Hit", "description": "snippet",
			}}},
		})
	}))
	t.Cleanup(provider.Close)
	spec := braveRESTSpec()
	spec.BuildURL = func(_ Settings, query string, _ int) (string, error) {
		return provider.URL + "?q=" + url.QueryEscape(query), nil
	}
	reg := NewRegistry(testCatalog(t))
	reg.Register(NewRESTSearchProvider(spec, KindKeyed))
	injectProviderHTTPClient(t, provider.Client())

	gate := recordGate(t, true)
	query := "notes " + named.URL + "/collect?d=payload"
	ctx := egressgate.WithAttribution(context.Background(), confine.EgressCommand{
		SessionID: "chat", ToolCallID: "call", Image: "web_search",
	})
	out := Search(ctx, SearchOptions{
		Query: query,
		Settings: Settings{
			Keys:             map[string]string{"brave": "brave-key"},
			EnabledProviders: []string{"brave"},
		},
		Registry:  reg,
		SessionID: "chat",
	})

	if !out.OK || len(out.Results) != 1 || out.Results[0].URL != result.URL+"/hit" {
		t.Fatalf("search result = %+v", out)
	}
	if got, _ := received.Load().(string); atProvider.Load() != 1 || got != query {
		t.Fatalf("provider received %d requests with query %q, want one with %q", atProvider.Load(), got, query)
	}
	if inQuery.Load() != 0 || inResult.Load() != 0 {
		t.Fatalf("requests outside the selected provider: address in query %d, returned result %d",
			inQuery.Load(), inResult.Load())
	}
	if dials := gate.reached(); len(dials) != 0 {
		t.Fatalf("a search's provider request reached the gate: %+v", dials)
	}
}
