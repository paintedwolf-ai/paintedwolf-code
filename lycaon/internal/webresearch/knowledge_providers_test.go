package webresearch

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestProviderWikidata(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("action"); got != "wbsearchentities" {
			t.Fatalf("action = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"search": []map[string]any{{
				"id": "Q7259", "concepturi": "http://www.wikidata.org/entity/Q7259",
				"url": "//www.wikidata.org/wiki/Q7259",
				"display": map[string]any{
					"label":       map[string]any{"value": "Ada Lovelace"},
					"description": map[string]any{"value": "English mathematician"},
				},
			}},
		})
	})
	spec := wikidataRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?action=wbsearchentities&search=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"wikidata": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "lovelace", 5)
	if !out.ok || out.hits[0].URL != "http://www.wikidata.org/entity/Q7259" {
		t.Fatalf("out = %+v", out)
	}
	if out.hits[0].Title != "Ada Lovelace (Q7259)" || out.hits[0].Snippet != "English mathematician" {
		t.Fatalf("hit = %+v", out.hits[0])
	}
}

func TestProviderWiktionary(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pages": []map[string]any{{
				"title": "ephemeral", "key": "ephemeral", "excerpt": "lasting a <span>short</span> time",
			}},
		})
	})
	spec := mediawikiRESTSpec("wiktionary")
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "/w/rest.php/v1/search/page?q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"wiktionary": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "ephemeral", 5)
	if !out.ok || out.hits[0].URL != srv.URL+"/wiki/ephemeral" {
		t.Fatalf("out = %+v", out)
	}
	if out.hits[0].Snippet != "lasting a short time" {
		t.Fatalf("snippet = %q", out.hits[0].Snippet)
	}
}

func TestProviderCrossref(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("mailto"); got != politeContactMailto {
			t.Fatalf("mailto = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{
				"items": []map[string]any{{
					"title": []string{"CRISPR gene editing"}, "DOI": "10.1/x",
					"URL":             "https://doi.org/10.1/x",
					"container-title": []string{"Nature"},
					"published":       map[string]any{"date-parts": [][]int{{2020, 5}}},
				}},
			},
		})
	})
	spec := crossrefRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?query=" + query + "&mailto=" + politeContactMailto, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"crossref": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "crispr", 5)
	if !out.ok || out.hits[0].URL != "https://doi.org/10.1/x" {
		t.Fatalf("out = %+v", out)
	}
	if out.hits[0].Snippet != "Nature · 2020" {
		t.Fatalf("snippet = %q", out.hits[0].Snippet)
	}
}

func TestProviderEuropePMC(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resultList": map[string]any{
				"result": []map[string]any{{
					"id": "42259350", "source": "MED", "title": "Malaria review",
					"journalTitle": "Lancet", "pubYear": "2024", "authorString": "Smith J.",
				}},
			},
		})
	})
	spec := europepmcRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?query=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"europepmc": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "malaria", 5)
	if !out.ok || out.hits[0].URL != "https://europepmc.org/article/MED/42259350" {
		t.Fatalf("out = %+v", out)
	}
}

func TestProviderInternetArchive(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		// description arrives as an array here; identifier/title as strings.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{
				"docs": []map[string]any{{
					"identifier": "apollo11", "title": "Apollo 11",
					"description": []string{"Moon landing", "1969"},
				}},
			},
		})
	})
	spec := internetArchiveRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"internetarchive": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "apollo", 5)
	if !out.ok || out.hits[0].URL != "https://archive.org/details/apollo11" {
		t.Fatalf("out = %+v", out)
	}
	if out.hits[0].Snippet != "Moon landing 1969" {
		t.Fatalf("snippet = %q", out.hits[0].Snippet)
	}
}

func TestProviderOpenLibrary(t *testing.T) {
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"docs": []map[string]any{{
				"key": "/works/OL61982W", "title": "The Odyssey",
				"author_name": []string{"Homer"}, "first_publish_year": 1488,
			}},
		})
	})
	spec := openLibraryRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "/search.json?q=" + query, nil
	}
	out := NewRESTSearchProvider(spec, KindKeyless).Search(context.Background(), Settings{
		Config:                map[string]map[string]string{"openlibrary": {"endpoint": srv.URL}},
		PerProviderTimeoutSec: 5,
	}, "odyssey", 5)
	if !out.ok || out.hits[0].URL != srv.URL+"/works/OL61982W" {
		t.Fatalf("out = %+v", out)
	}
	if out.hits[0].Snippet != "Homer · 1488" {
		t.Fatalf("snippet = %q", out.hits[0].Snippet)
	}
}
