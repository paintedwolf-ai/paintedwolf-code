package webresearch

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestProviderStackExchange(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("site"); got != "serverfault" {
			t.Fatalf("site = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{
				"title": "How to foo", "link": "https://serverfault.com/q/1",
				"score": 10, "is_answered": true,
			}},
			"quota_remaining": 300,
		})
	})
	spec := stackexchangeRESTSpec("stackexchange_serverfault", "serverfault")
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?site=serverfault&q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"stackexchange_serverfault": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "foo", 5)
	if !out.ok || out.hits[0].Provider != "stackexchange_serverfault" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderStackExchangeAppleSiteBaked(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("site"); got != "apple" {
			t.Fatalf("site = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{
				"title": "macOS window", "link": "https://apple.stackexchange.com/q/1",
				"score": 4, "is_answered": true,
			}},
			"quota_remaining": 300,
		})
	})
	spec := stackexchangeRESTSpec("stackexchange_apple", "apple")
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?site=apple&q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"stackexchange_apple": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "macos", 5)
	if !out.ok || out.hits[0].URL != "https://apple.stackexchange.com/q/1" {
		t.Fatalf("out = %+v", out)
	}
}

func TestStackExchangeVariantUsesResolvedSharedCredential(t *testing.T) {
	spec := stackexchangeRESTSpec("stackexchange_unix", "unix")
	rawURL, err := spec.BuildURL(Settings{
		Keys:   map[string]string{"stackexchange_unix": "shared-key"},
		Config: map[string]map[string]string{"stackexchange_unix": {"endpoint": "https://api.stackexchange.com"}},
	}, "systemd", 5)
	if err != nil {
		t.Fatalf("BuildURL: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if got := req.URL.Query().Get("key"); got != "shared-key" {
		t.Fatalf("key = %q", got)
	}
}

func TestStackExchangeVariantsSharePacingGate(t *testing.T) {
	reg := testRegistry(t)
	var shared *providerGate
	for _, id := range []string{
		"stackexchange",
		"stackexchange_apple",
		"stackexchange_superuser",
		"stackexchange_askubuntu",
		"stackexchange_serverfault",
		"stackexchange_unix",
	} {
		backoff, ok := reg.Get(id).(*queryBackoffProvider)
		if !ok {
			t.Fatalf("%s provider is %T", id, reg.Get(id))
		}
		health, ok := backoff.inner.(*healthWrappedProvider)
		if !ok {
			t.Fatalf("%s backoff inner is %T", id, backoff.inner)
		}
		provider, ok := health.inner.(*gatedSearchProvider)
		if !ok {
			t.Fatalf("%s health inner is %T", id, health.inner)
		}
		if shared == nil {
			shared = provider.gate
			continue
		}
		if provider.gate != shared {
			t.Fatalf("%s has a distinct pacing gate", id)
		}
	}
}

func TestProviderMicrosoftLearnMCP(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		if got := r.Header.Get("Accept"); got != microsoftLearnAcceptHeader {
			t.Fatalf("Accept = %q", got)
		}
		_, _ = w.Write([]byte(`event: message
data: {"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"results\":[{\"title\":\"Azure Functions\",\"content\":\"Timeout docs.\",\"contentUrl\":\"https://learn.microsoft.com/azure/azure-functions/functions-scale\"}]}"}]}}
`))
	})
	spec := microsoftLearnRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"microsoft_learn": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "azure functions timeout", 5)
	if !out.ok || out.hits[0].URL != "https://learn.microsoft.com/azure/azure-functions/functions-scale" {
		t.Fatalf("out = %+v", out)
	}
}

func TestParseMicrosoftLearnHitsDedupesURLs(t *testing.T) {
	body := []byte(`event: message
data: {"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"results\":[{\"title\":\"A\",\"content\":\"one\",\"contentUrl\":\"https://learn.microsoft.com/a\"},{\"title\":\"A\",\"content\":\"two\",\"contentUrl\":\"https://learn.microsoft.com/a\"}]}"}]}}
`)
	hits, err := parseMicrosoftLearnHits(body)
	if err != nil {
		t.Fatalf("parseMicrosoftLearnHits: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d want 1", len(hits))
	}
}

func TestStackExchangeGateRecordStatus(t *testing.T) {
	if st, ok := stackExchangeGateRecordStatus([]byte(`{"quota_remaining":3}`)); !ok || st != 403 {
		t.Fatalf("status = %d ok = %v", st, ok)
	}
	if _, ok := stackExchangeGateRecordStatus([]byte(`{"quota_remaining":100}`)); ok {
		t.Fatal("expected no gate record for healthy quota")
	}
}

func TestProviderArxivAtom(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <title>Paper</title>
    <id>https://arxiv.org/abs/1234.5678</id>
    <summary>Abstract text here.</summary>
  </entry>
</feed>`))
	})
	spec := arxivRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "/api/query?search_query=all:" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"arxiv": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "paper", 5)
	if !out.ok || out.hits[0].URL != "https://arxiv.org/abs/1234.5678" {
		t.Fatalf("out = %+v", out)
	}
}

func TestParseAtomHitsMalformed(t *testing.T) {
	if _, err := parseAtomHits([]byte("not xml"), "arxiv", 300); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestProviderGithubKeyless(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("unexpected auth on keyless github search")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{
				"title": "Bug", "html_url": "https://github.com/o/r/issues/1",
				"body": "details", "state": "open", "comments": 3,
			}},
		})
	})
	spec := githubRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "/search/issues?q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		PerProviderTimeoutSec: 5,
	}, "bug", 5)
	if !out.ok || out.hits[0].URL != "https://github.com/o/r/issues/1" {
		t.Fatalf("out = %+v", out)
	}
}
