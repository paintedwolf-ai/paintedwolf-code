package webresearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func withMockProviderHTTP(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	injectProviderHTTPClient(t, srv.Client())
	t.Cleanup(srv.Close)
	return srv
}

// injectProviderHTTPClient relaxes provider egress for httptest URLs and installs
// the given client as the post-validation dial transport.
func injectProviderHTTPClient(t *testing.T, client *http.Client) {
	t.Helper()
	allowLoopbackFetch(t)
	prevRelax := providerEgressTestRelax
	providerEgressTestRelax = true
	prev := providerHTTPClient
	providerHTTPClient = client
	t.Cleanup(func() {
		providerHTTPClient = prev
		providerEgressTestRelax = prevRelax
	})
}

func TestProviderGoogleCSE(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-goog-api-key"); got != "secret" {
			t.Fatalf("api key = %q, want %q", got, "secret")
		}
		if got := r.URL.Query().Get("cx"); got != "cx123" {
			t.Fatalf("cx = %q", got)
		}
		if r.URL.Query().Has("key") {
			t.Fatalf("query has key parameter: %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]string{{
				"title": "Hit", "link": "https://example.com/hit", "snippet": "snippet",
			}},
		})
	})
	spec := googleCseRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?cx=cx123&q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyedExtra).Search(context.Background(), Settings{
		Keys:                  map[string]string{"google_cse": "secret"},
		Config:                map[string]map[string]string{"google_cse": {"search_engine_id": "cx123"}},
		PerProviderTimeoutSec: 5,
	}, "query", 5)
	if !out.ok || len(out.hits) != 1 || out.hits[0].Provider != "google_cse" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderGoogleCSEMissingCXNotConfigured(t *testing.T) {
	reg := testRegistry(t)
	result := Search(context.Background(), SearchOptions{
		Query: "test",
		Settings: Settings{
			Keys:             map[string]string{"google_cse": "secret"},
			EnabledProviders: []string{"google_cse"},
		},
		Registry: reg,
	})
	if len(result.ProvidersSkipped) != 1 || result.ProvidersSkipped[0].Reason != "not_configured" {
		t.Fatalf("skipped = %+v", result.ProvidersSkipped)
	}
}

func TestProviderSerper(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-API-KEY"); got != "serper-key" {
			t.Fatalf("key = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"organic": []map[string]string{{
				"title": "Hit", "link": "https://example.com/serper", "snippet": "snippet",
			}},
		})
	})
	spec := serperRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyed).Search(context.Background(), Settings{
		Keys:                  map[string]string{"serper": "serper-key"},
		PerProviderTimeoutSec: 5,
	}, "query", 5)
	if !out.ok || out.hits[0].Provider != "serper" {
		t.Fatalf("out = %+v", out)
	}
}

func TestSearchExplicitSerperCustomMode(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"organic": []map[string]string{{
				"title": "Hit", "link": "https://example.com/only-serper", "snippet": "snippet",
			}},
		})
	})
	spec := serperRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL, nil
	}
	reg := NewRegistry(testCatalog(t))
	reg.Register(NewRESTSearchProvider(spec, KindKeyed))
	result := Search(context.Background(), SearchOptions{
		Query:    "test",
		Provider: "serper",
		Settings: Settings{
			Keys: map[string]string{"serper": "key"},
		},
		Registry: reg,
	})
	if len(result.Results) != 1 || result.Results[0].URL != "https://example.com/only-serper" {
		t.Fatalf("result = %+v", result)
	}
}

func TestProviderSearxng(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			t.Fatalf("format = %q", r.URL.Query().Get("format"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]string{{
				"title": "Hit", "url": "https://example.com/searx", "content": "snippet",
			}},
		})
	})
	spec := searxngRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "/search?q=" + query + "&format=json", nil
	}
	out := NewRESTSearchProvider(spec, KindKeylessEndpoint).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"searxng": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "query", 5)
	if !out.ok || out.hits[0].Provider != "searxng" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderMwmbl(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("s") == "" {
			http.Error(w, "missing query", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"url":     "https://example.com/mwmbl",
			"title":   []map[string]string{{"value": "Hit"}},
			"extract": []map[string]string{{"value": "snippet"}},
		}})
	})
	spec := mwmblRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "/api/v1/search/?s=" + url.QueryEscape(query), nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		PerProviderTimeoutSec: 5,
	}, "query", 5)
	if !out.ok || out.hits[0].Provider != "mwmbl" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderTavily(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tavily-key" {
			t.Fatalf("auth = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if _, ok := body["api_key"]; ok {
			t.Fatalf("body contains api_key: %+v", body)
		}
		if body["query"] != "query" || body["max_results"] != float64(5) {
			t.Fatalf("body = %+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]string{{
				"title": "Hit", "url": "https://example.com/tavily", "content": "snippet",
			}},
		})
	})
	spec := tavilyRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyed).Search(context.Background(), Settings{
		Keys:                  map[string]string{"tavily": "tavily-key"},
		PerProviderTimeoutSec: 5,
	}, "query", 5)
	if !out.ok || out.hits[0].Provider != "tavily" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderKagi(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer kagi-key" {
			t.Fatalf("auth = %q", got)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["query"] != "query" || body["workflow"] != "search" || body["format"] != "json" || body["limit"] != float64(5) {
			t.Fatalf("body = %+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"search": []map[string]string{{
					"title": "Hit", "url": "https://example.com/kagi", "snippet": "snippet",
				}},
			},
		})
	})
	spec := kagiRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyed).Search(context.Background(), Settings{
		Keys:                  map[string]string{"kagi": "kagi-key"},
		PerProviderTimeoutSec: 5,
	}, "query", 5)
	if !out.ok || out.hits[0].Provider != "kagi" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderGithub(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer github-key" {
			t.Fatalf("auth = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github.text-match+json" {
			t.Fatalf("accept = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{
				"path":         "src/main.go",
				"html_url":     "https://github.com/owner/repo/blob/main/src/main.go",
				"repository":   map[string]string{"full_name": "owner/repo"},
				"text_matches": []map[string]string{{"fragment": "func main() {}"}},
			}},
		})
	})
	spec := githubRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Keys:                  map[string]string{"github": "github-key"},
		PerProviderTimeoutSec: 5,
	}, "main", 5)
	if !out.ok || out.hits[0].Provider != "github" {
		t.Fatalf("out = %+v", out)
	}
	if out.hits[0].Title != "owner/repo/src/main.go" || out.hits[0].Snippet != "func main() {}" {
		t.Fatalf("hit = %+v", out.hits[0])
	}
}

func TestProviderGitlab(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer gitlab-key" {
			t.Fatalf("auth = %q", got)
		}
		if got := r.URL.Query().Get("simple"); got != "true" {
			t.Fatalf("simple = %q", got)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name_with_namespace": "Group / Repo",
			"path_with_namespace": "group/repo",
			"web_url":             "https://gitlab.com/group/repo",
			"description":         "a project",
		}})
	})
	spec := gitlabRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?simple=true&search=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Keys:                  map[string]string{"gitlab": "gitlab-key"},
		PerProviderTimeoutSec: 5,
	}, "repo", 5)
	if !out.ok || out.hits[0].Provider != "gitlab" {
		t.Fatalf("out = %+v", out)
	}
	if out.hits[0].URL != "https://gitlab.com/group/repo" || out.hits[0].Title != "Group / Repo" {
		t.Fatalf("hit = %+v", out.hits[0])
	}
}

func TestRegistryRegistersAllCatalogProviders(t *testing.T) {
	reg := testRegistry(t)
	if len(reg.IDs()) != len(testCatalog(t).Entries()) {
		t.Fatalf("ids = %v want %d", reg.IDs(), len(testCatalog(t).Entries()))
	}
}

func TestProviderHN(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != providerSearchUserAgent {
			t.Fatalf("user-agent = %q", got)
		}
		if r.URL.Query().Get("tags") != "story" {
			t.Fatalf("tags = %q", r.URL.Query().Get("tags"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hits": []map[string]any{{
				"title": "Show HN", "url": "https://example.com/story", "objectID": "42",
				"points": 100, "num_comments": 12,
			}},
		})
	})
	spec := hnRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "/api/v1/search?query=" + query + "&tags=story&hitsPerPage=5", nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		PerProviderTimeoutSec: 5,
	}, "query", 5)
	if !out.ok || out.hits[0].URL != "https://example.com/story" {
		t.Fatalf("out = %+v", out)
	}
	if !strings.Contains(out.hits[0].Snippet, "100 points") {
		t.Fatalf("snippet = %q", out.hits[0].Snippet)
	}
}

func TestProviderHNItemFallback(t *testing.T) {
	body := []byte(`{"hits":[{"title":"Ask HN","objectID":"99","points":1,"num_comments":2}]}`)
	hits, err := parseHNHits(body)
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits = %+v err = %v", hits, err)
	}
	if hits[0].URL != "https://news.ycombinator.com/item?id=99" {
		t.Fatalf("url = %q", hits[0].URL)
	}
}

func TestProviderWikipedia(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pages": []map[string]string{{
				"title": "Go", "key": "Go_(programming_language)",
				"excerpt": "Go is <span class=\"searchmatch\">fast</span>.",
			}},
		})
	})
	spec := mediawikiRESTSpec("wikipedia")
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "/w/rest.php/v1/search/page?q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"wikipedia": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "go", 5)
	if !out.ok || !strings.Contains(out.hits[0].URL, "/wiki/Go_") {
		t.Fatalf("out = %+v", out)
	}
	if strings.Contains(out.hits[0].Snippet, "<span") {
		t.Fatalf("snippet still has html: %q", out.hits[0].Snippet)
	}
}

func TestProviderMDN(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"documents": []map[string]string{{
				"title": "Fetch", "mdn_url": "/en-US/docs/Web/API/Fetch_API", "summary": "network",
			}},
		})
	})
	spec := mdnRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "/api/v1/search?q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"mdn": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "fetch", 5)
	if !out.ok || out.hits[0].URL != srv.URL+"/en-US/docs/Web/API/Fetch_API" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderGDELT(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("mode"); got != "ArtList" {
			t.Fatalf("mode = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"articles": []map[string]string{{
				"url": "https://example.com/story", "title": "Headline", "domain": "example.com", "language": "English",
			}},
		})
	})
	spec := gdeltRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "/api/v2/doc/doc?query=" + query + "&mode=ArtList&maxrecords=5&format=json", nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"gdelt": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "news", 5)
	if !out.ok || out.hits[0].URL != "https://example.com/story" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderOpenAlex(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("mailto"); got != openAlexPoliteMailto {
			t.Fatalf("mailto = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{
				"title": "Paper", "publication_year": 2024,
				"primary_location": map[string]string{"landing_page_url": "https://example.org/paper"},
			}},
		})
	})
	spec := openalexRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "/works?search=" + query + "&per_page=5&mailto=" + openAlexPoliteMailto, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"openalex": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "paper", 5)
	if !out.ok || out.hits[0].URL != "https://example.org/paper" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderHuggingFace(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"modelId": "org/model", "pipeline_tag": "text-generation", "downloads": 100, "likes": 5,
		}})
	})
	spec := huggingfaceRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "/api/models?search=" + query + "&limit=5", nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"huggingface": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "model", 5)
	if !out.ok || out.hits[0].URL != srv.URL+"/org/model" {
		t.Fatalf("out = %+v", out)
	}
}
