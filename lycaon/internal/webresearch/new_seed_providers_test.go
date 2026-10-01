package webresearch

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestProviderMankier(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "systemd" {
			t.Fatalf("q = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{
				"name": "systemd", "section": "1",
				"description": "systemd system and service manager",
				"url":         "https://www.mankier.com/1/systemd",
			}},
		})
	})
	spec := mankierRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "/api/v2/mans/?q=" + query + "&limit=3", nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"mankier": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "systemd", 5)
	if !out.ok || out.hits[0].URL != "https://www.mankier.com/1/systemd" {
		t.Fatalf("out = %+v", out)
	}
	if out.hits[0].Title != "systemd(1)" {
		t.Fatalf("title = %q", out.hits[0].Title)
	}
}

func TestProviderIETFRFC(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("type") != "rfc" || q.Get("format") != "json" {
			t.Fatalf("query = %v", q)
		}
		title := q.Get("title__icontains")
		abstract := q.Get("abstract__icontains")
		name := q.Get("name__icontains")
		objects := []map[string]any{}
		switch {
		case title == "HTTP Semantics" || abstract == "HTTP Semantics":
			objects = append(objects, map[string]any{
				"name": "rfc9110", "title": "HTTP Semantics", "rfc": "9110", "rfc_number": 9110,
				"abstract": "The Hypertext Transfer Protocol (HTTP) is a stateless application-level protocol.",
			})
		case name == "rfc9110":
			objects = append(objects, map[string]any{
				"name": "rfc9110", "title": "HTTP Semantics", "rfc": "9110", "rfc_number": 9110,
				"abstract": "The Hypertext Transfer Protocol (HTTP) is a stateless application-level protocol.",
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"objects": objects})
	})
	p := newIETFRFCProvider()
	out := p.Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"ietf_rfc": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "HTTP Semantics", 5)
	if !out.ok || len(out.hits) != 1 {
		t.Fatalf("out = %+v", out)
	}
	if out.hits[0].URL != "https://www.rfc-editor.org/rfc/rfc9110.html" {
		t.Fatalf("url = %q", out.hits[0].URL)
	}
	if !strings.Contains(out.hits[0].Title, "RFC 9110") {
		t.Fatalf("title = %q", out.hits[0].Title)
	}

	out2 := p.Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"ietf_rfc": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "rfc9110", 5)
	if !out2.ok || len(out2.hits) != 1 || out2.hits[0].URL != "https://www.rfc-editor.org/rfc/rfc9110.html" {
		t.Fatalf("name search out = %+v", out2)
	}
}

func TestProviderNixOSWiki(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pages": []map[string]any{{
				"title": "Flakes", "key": "Flakes",
				"excerpt": "Nix <span>flakes</span>",
			}},
		})
	})
	spec := mediawikiRESTSpec("nixos_wiki")
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "/w/rest.php/v1/search/page?q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"nixos_wiki": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "flakes", 5)
	if !out.ok || out.hits[0].URL != srv.URL+"/wiki/Flakes" {
		t.Fatalf("out = %+v", out)
	}
	if strings.Contains(out.hits[0].Snippet, "<span") {
		t.Fatalf("snippet still has html: %q", out.hits[0].Snippet)
	}
}

func TestProviderGentooWiki(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("action"); got != "query" {
			t.Fatalf("action = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"query": map[string]any{
				"search": []map[string]any{{
					"title": "Systemd/systemd-nspawn", "snippet": "container <span>tool</span>",
				}},
			},
		})
	})
	spec := mediawikiActionSpec("gentoo_wiki")
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "/api.php?action=query&list=search&srsearch=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"gentoo_wiki": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "systemd", 5)
	if !out.ok || out.hits[0].URL != srv.URL+"/wiki/Systemd/systemd-nspawn" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderDiscourseRust(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("Accept = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"posts": []map[string]any{{
				"topic_id": 140230, "blurb": "After Rust 1.75, we still need async_trait?",
			}},
			"topics": []map[string]any{{
				"id": 140230, "slug": "after-rust-1-75-we-still-need-to-use-async-trait",
				"title": "Do we still need async_trait?",
			}},
		})
	})
	spec := discourseRESTSpec("discourse_rust")
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "/search.json?q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"discourse_rust": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "asynctrait", 5)
	want := srv.URL + "/t/after-rust-1-75-we-still-need-to-use-async-trait/140230"
	if !out.ok || out.hits[0].URL != want {
		t.Fatalf("out = %+v", out)
	}
}

func TestMediaWikiPageURLKeepsSlashSegments(t *testing.T) {
	got := mediaWikiPageURL("https://wiki.gentoo.org", "Systemd/systemd-nspawn")
	if got != "https://wiki.gentoo.org/wiki/Systemd/systemd-nspawn" {
		t.Fatalf("got %q", got)
	}
}
