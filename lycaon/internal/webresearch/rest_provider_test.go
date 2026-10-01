package webresearch

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestRESTProviderClassifiesItsOwnIODeadlineAsTimeout(t *testing.T) {
	srv := withMockProviderHTTP(t, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	spec := RESTSpec{
		ProviderID: "deadline_test",
		Method:     http.MethodGet,
		BuildURL: func(Settings, string, int) (string, error) {
			return srv.URL, nil
		},
		ParseHits: func([]byte) ([]WebHit, error) { return nil, nil },
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		PerProviderTimeoutSec: 1,
	}, "query", 1)
	if out.reason != "timeout" {
		t.Fatalf("reason = %q detail = %q want timeout", out.reason, out.detail)
	}
}

func TestRESTProviderDropsInvalidResultURLs(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]WebHit{
			{Title: "Missing"},
			{Title: "Script", URL: "javascript:alert(1)"},
			{Title: "Credentials", URL: "https://user:pass@example.com/private"},
			{Title: "Valid", URL: "  https://example.com/docs  "},
		})
	})
	spec := RESTSpec{
		ProviderID: "url_test",
		Method:     http.MethodGet,
		BuildURL: func(Settings, string, int) (string, error) {
			return srv.URL, nil
		},
		ParseHits: func(body []byte) ([]WebHit, error) {
			var hits []WebHit
			err := json.Unmarshal(body, &hits)
			return hits, err
		},
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{}, "query", 10)
	if !out.ok || len(out.hits) != 1 || out.hits[0].URL != "https://example.com/docs" {
		t.Fatalf("out = %+v", out)
	}
}

func TestRESTProviderRejectsResponseWithOnlyInvalidResultURLs(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]WebHit{{Title: "Missing"}})
	})
	spec := RESTSpec{
		ProviderID: "invalid_only_test",
		Method:     http.MethodGet,
		BuildURL: func(Settings, string, int) (string, error) {
			return srv.URL, nil
		},
		ParseHits: func(body []byte) ([]WebHit, error) {
			var hits []WebHit
			err := json.Unmarshal(body, &hits)
			return hits, err
		},
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{}, "query", 10)
	if out.ok || out.reason != "parse_error" {
		t.Fatalf("out = %+v", out)
	}
}
