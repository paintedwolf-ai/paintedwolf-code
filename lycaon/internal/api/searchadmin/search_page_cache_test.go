package searchadmin

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func completeSearchResponse(hitIDs ...string) wire.SearchResponse {
	hits := make([]wire.SearchHit, 0, len(hitIDs))
	for _, id := range hitIDs {
		hits = append(hits, wire.SearchHit{HitID: id, HitKind: "code", Source: "code", ProjectID: "p", Title: id})
	}
	return wire.SearchResponse{
		Hits:             hits,
		Status:           wire.SearchResultStatusComplete,
		Exhaustive:       true,
		TotalHits:        len(hits),
		CountRelation:    wire.SearchCountRelationExact,
		FacetsExhaustive: true,
	}
}

func TestSearchResponsePagePreservesGenerationMetadata(t *testing.T) {
	response := completeSearchResponse("one", "two", "three")
	page, err := searchResponsePage(response, 7, 0, 2, "scope")
	if err != nil {
		testutil.FailErr(t, "page response", err)
	}
	if len(page.Hits) != 2 || page.TotalHits != 3 || !page.Exhaustive || page.CountRelation != wire.SearchCountRelationExact {
		t.Fatalf("page = %+v", page)
	}
	if page.NextCursor == "" {
		t.Fatal("first page did not expose a continuation cursor")
	}
	last, err := searchResponsePage(response, 7, 2, 2, "scope")
	if err != nil {
		testutil.FailErr(t, "last page response", err)
	}
	if len(last.Hits) != 1 || last.NextCursor != "" || last.Hits[0].HitID != "three" {
		t.Fatalf("last page = %+v", last)
	}
}

func TestSearchPageCacheBoundsAndRenewsGenerations(t *testing.T) {
	cache := newSearchPageCache()
	var first uint64
	for i := 0; i < searchPageMaxGenerations+1; i++ {
		generation := cache.put("scope", completeSearchResponse("hit"))
		if i == 0 {
			first = generation
		}
	}
	if _, ok := cache.get(first, "scope"); ok {
		t.Fatal("oldest generation survived the cache bound")
	}
	latest := cache.generation
	entry := cache.entries[latest]
	entry.expiresAt = time.Now().Add(time.Second)
	cache.entries[latest] = entry
	if _, ok := cache.get(latest, "scope"); !ok {
		t.Fatal("latest generation was not found")
	}
	if time.Until(cache.entries[latest].expiresAt) < searchPageLifetime-time.Minute {
		t.Fatal("reading a generation did not renew its lifetime")
	}
	if _, ok := cache.get(latest, "different-scope"); ok {
		t.Fatal("generation escaped its request scope")
	}
}

func TestWireSearchResponseCarriesStableIdentityAndCompletion(t *testing.T) {
	response := toWireSearchResponse(&search.Result{
		Hits:          []search.Hit{{ID: "stable-1", HitKind: "code", Source: "code", ProjectID: "p", RootID: "root", Title: "stable"}},
		Status:        search.ResultStatusLimited,
		CountRelation: search.CountRelationLowerBound,
		Issues: []search.Issue{{
			Executor: "federation",
			Reason:   search.IssueResultLimit,
			Limit:    20_000,
		}},
	})
	if len(response.Hits) != 1 || response.Hits[0].HitID != "stable-1" || response.Hits[0].RootID != "root" {
		t.Fatalf("hits = %+v", response.Hits)
	}
	if response.Status != wire.SearchResultStatusLimited || response.Exhaustive || response.CountRelation != wire.SearchCountRelationLowerBound {
		t.Fatalf("completion = (%q, %v, %q)", response.Status, response.Exhaustive, response.CountRelation)
	}
	if response.FacetsExhaustive || len(response.Issues) != 1 || response.Issues[0].Limit != 20_000 {
		t.Fatalf("issues = %+v, facets_exhaustive = %v", response.Issues, response.FacetsExhaustive)
	}
}
