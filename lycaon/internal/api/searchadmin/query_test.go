package searchadmin

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSearchPagesThroughOneRetainedGeneration(t *testing.T) {
	f := newSearchFixture(t)
	alpha := f.addProject(t, "Alpha App", "alpha-root", map[string]string{"readme.txt": "nothing to see\n"})
	for _, id := range []string{"p1", "p2", "p3"} {
		f.seedRow(t, search.IndexRow{ID: id, ProjectID: alpha.ID, Source: search.SourceTool, HitKind: search.HitKindWeb,
			Snippet: "pager-token " + id})
	}
	req := wire.SearchRequest{Query: "pager-token", OriginProjectID: alpha.ID, Limit: 2}
	first := decodeOK[wire.SearchResponse](t, f.post(t, "/v1/search", jsonBody(t, req)))
	if len(first.Hits) != 2 || first.TotalHits != 3 || first.NextCursor == "" {
		t.Fatalf("first page = %d hits of %d, cursor %q", len(first.Hits), first.TotalHits, first.NextCursor)
	}
	if len(first.Facets) == 0 || len(first.Histogram) == 0 {
		t.Fatalf("first page lost facets or histogram: %+v", first)
	}
	for _, hit := range first.Hits {
		if hit.ProjectID != alpha.ID || hit.ProjectName != "Alpha App" || hit.CreatedAt.IsZero() {
			t.Fatalf("hit lost project identity or timestamp: %+v", hit)
		}
	}

	req.Cursor = first.NextCursor
	second := decodeOK[wire.SearchResponse](t, f.post(t, "/v1/search", jsonBody(t, req)))
	if len(second.Hits) != 1 || second.NextCursor != "" || second.TotalHits != 3 {
		t.Fatalf("second page = %d hits, cursor %q, total %d", len(second.Hits), second.NextCursor, second.TotalHits)
	}
	seen := map[string]bool{}
	for _, hit := range append(first.Hits, second.Hits...) {
		if seen[hit.HitID] {
			t.Fatalf("hit %q served twice across pages", hit.HitID)
		}
		seen[hit.HitID] = true
	}

	scope, err := searchRequestScope(req)
	testutil.FailErr(t, "derive search scope", err)
	past, err := globalSearchPages.EncodeAt(scope, f.handler.searchPages.generation, 99)
	testutil.FailErr(t, "encode past-end cursor", err)
	req.Cursor = past
	end := decodeOK[wire.SearchResponse](t, f.post(t, "/v1/search", jsonBody(t, req)))
	if len(end.Hits) != 0 || end.NextCursor != "" {
		t.Fatalf("past-end page = %+v", end)
	}
}

func TestSearchCursorRefusals(t *testing.T) {
	f := newSearchFixture(t)
	p := f.addProject(t, "Cursor", "cursor-root", map[string]string{"a.txt": "unrelated\n"})
	for _, id := range []string{"c1", "c2"} {
		f.seedRow(t, search.IndexRow{ID: id, ProjectID: p.ID, Source: search.SourceTool, HitKind: search.HitKindWeb,
			Snippet: "cursor-token " + id})
	}
	req := wire.SearchRequest{Query: "cursor-token", OriginProjectID: p.ID, Limit: 1}
	first := decodeOK[wire.SearchResponse](t, f.post(t, "/v1/search", jsonBody(t, req)))
	if first.NextCursor == "" {
		t.Fatalf("first page has no continuation: %+v", first)
	}

	t.Run("generation not retained by this host", func(t *testing.T) {
		restarted := f.mount(New(f.handler.responses, Dependencies{Database: f.database, Projects: f.projects}))
		next := req
		next.Cursor = first.NextCursor
		rec := postTo(t, restarted, "/v1/search", jsonBody(t, next))
		requireErrorCode(t, rec, wire.ApiErrorCodeCursorGenerationExpired)
	})
	t.Run("cursor from another query", func(t *testing.T) {
		other := wire.SearchRequest{Query: "different-token", OriginProjectID: p.ID, Cursor: first.NextCursor}
		requireErrorCode(t, f.post(t, "/v1/search", jsonBody(t, other)), wire.ApiErrorCodeInvalidPageCursor)
	})
	t.Run("negative position", func(t *testing.T) {
		scope, err := searchRequestScope(req)
		testutil.FailErr(t, "derive search scope", err)
		cursor, err := globalSearchPages.EncodeAt(scope, f.handler.searchPages.generation, -1)
		testutil.FailErr(t, "encode negative cursor", err)
		next := req
		next.Cursor = cursor
		requireErrorCode(t, f.post(t, "/v1/search", jsonBody(t, next)), wire.ApiErrorCodeInvalidPageCursor)
	})
}

func TestSearchRequestRefusals(t *testing.T) {
	f := newSearchFixture(t)
	f.addProject(t, "Refusals", "refusals-root", nil)
	for _, tc := range []struct {
		name string
		body string
		want wire.ApiErrorCode
	}{
		{"malformed body", `{"query":`, wire.ApiErrorCodeInvalidJson},
		{"blank query", `{"query":"  "}`, wire.ApiErrorCodeInvalidRequest},
		{"limit above range", `{"query":"x","limit":501}`, wire.ApiErrorCodeInvalidRequest},
		{"limit below range", `{"query":"x","limit":-1}`, wire.ApiErrorCodeInvalidRequest},
		{"unknown budget", `{"query":"x","budget":"eventually"}`, wire.ApiErrorCodeInvalidRequest},
		{"unknown origin", `{"query":"x","origin_project_id":"missing-project"}`, wire.ApiErrorCodeProjectNotFound},
		{"invalid regex", `{"query":"\"a[\"","regex":true}`, wire.ApiErrorCodeSearchPatternInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireErrorCode(t, f.post(t, "/v1/search", tc.body), tc.want)
		})
	}
	t.Run("parse error names the field", func(t *testing.T) {
		resp := requireErrorCode(t, f.post(t, "/v1/search", `{"query":"alpha kind:"}`), wire.ApiErrorCodeSearchQueryInvalid)
		if resp.Details["field"] != "kind" || resp.Details["kind"] != string(search.ParseErrEmptyValue) {
			t.Fatalf("details = %+v", resp.Details)
		}
	})
}

func TestSearchResolvesProjectScopeBySlugAndRootLabel(t *testing.T) {
	f := newSearchFixture(t)
	alpha := f.addProject(t, "Alpha App", "alpha-root", nil)
	beta := f.addProject(t, "Beta", "beta-root", nil)
	f.seedRow(t, search.IndexRow{ID: "a", ProjectID: alpha.ID, Source: search.SourceTool, HitKind: search.HitKindWeb, Snippet: "slug-token alpha"})
	f.seedRow(t, search.IndexRow{ID: "b", ProjectID: beta.ID, Source: search.SourceTool, HitKind: search.HitKindWeb, Snippet: "slug-token beta"})
	for _, tc := range []struct {
		name, query, wantProject string
	}{
		{"name slug", "project:alpha-app slug-token", alpha.ID},
		{"root label", "project:BETA-ROOT slug-token", beta.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := decodeOK[wire.SearchResponse](t, f.post(t, "/v1/search", jsonBody(t, wire.SearchRequest{Query: tc.query})))
			if resp.Interpreted.Scope != wire.SearchScopeMode(search.ScopeSlug) || len(resp.Hits) == 0 {
				t.Fatalf("scope = %q hits = %d", resp.Interpreted.Scope, len(resp.Hits))
			}
			for _, hit := range resp.Hits {
				if hit.ProjectID != tc.wantProject {
					t.Fatalf("hit escaped project scope: %+v", hit)
				}
			}
		})
	}
	t.Run("unknown slug", func(t *testing.T) {
		resp := requireErrorCode(t, f.post(t, "/v1/search", `{"query":"project:nowhere slug-token"}`), wire.ApiErrorCodeSearchQueryInvalid)
		if resp.Details["field"] != "project" {
			t.Fatalf("details = %+v", resp.Details)
		}
	})
}

func TestSearchResolvesWorkerNavigation(t *testing.T) {
	f := newSearchFixture(t)
	p := f.addProject(t, "Workers", "workers-root", nil)
	testdbseed.InsertSession(t, f.database, "parent-sess", p.ID)
	testdbseed.InsertSession(t, f.database, "child-sess", p.ID)
	_, err := f.database.ExecContext(t.Context(), `
		INSERT INTO worker_jobs (
			id, project_id, workspace_path, agent_type, status, prompt, brief, created_at,
			parent_session_id, child_session_id
		) VALUES ('worker-1', ?, ?, 'implementer', 'complete', 'fixture', 'fixture', ?, 'parent-sess', 'child-sess')
	`, p.ID, p.Roots[0].Path, time.Now().UTC().Format(time.RFC3339Nano))
	testutil.FailErr(t, "insert worker job", err)
	f.seedRow(t, search.IndexRow{ID: "w1", ProjectID: p.ID, Source: search.SourceMessage, HitKind: search.HitKindEvidence,
		SessionID: "child-sess", Snippet: "worker-nav-token child"})
	f.seedRow(t, search.IndexRow{ID: "w2", ProjectID: p.ID, Source: search.SourceMessage, HitKind: search.HitKindEvidence,
		SessionID: "parent-sess", Snippet: "worker-nav-token parent"})

	resp := decodeOK[wire.SearchResponse](t, f.post(t, "/v1/search",
		jsonBody(t, wire.SearchRequest{Query: "worker-nav-token", OriginProjectID: p.ID, Budget: wire.SearchBudget("complete")})))
	bySession := map[string]wire.SearchHit{}
	for _, hit := range resp.Hits {
		bySession[hit.SessionID] = hit
	}
	if child := bySession["child-sess"]; child.ParentSessionID != "parent-sess" || child.WorkerID != "worker-1" {
		t.Fatalf("child hit = %+v", child)
	}
	if parent := bySession["parent-sess"]; parent.HitID == "" || parent.ParentSessionID != "" || parent.WorkerID != "" {
		t.Fatalf("parent hit = %+v", parent)
	}
}

func TestWireSearchResponseCarriesInterpretationAndAggregates(t *testing.T) {
	if got := toWireSearchResponse(nil); got.Hits != nil || got.TotalHits != 0 {
		t.Fatalf("nil result = %+v", got)
	}
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	response := toWireSearchResponse(&search.Result{
		Hits: []search.Hit{
			{ID: "h1", TS: ts.Format(time.RFC3339Nano), TitleHighlights: []search.TextRange{{Start: 1, End: 3}}},
			{ID: "h2", TS: "not a time"},
		},
		Exhaustive: true,
		Facets:     []search.Facet{{Key: "kind", Values: []search.FacetValue{{Value: "web", Count: 2}}}},
		Histogram:  []search.HistogramBucket{{Key: "web", Count: 2}},
		Interpretation: search.SearchInterpretation{
			Scope:   search.ScopeGlobal,
			Filters: []search.InterpretedFilter{{Field: "kind", Value: "web", Negated: true}},
		},
	})
	if !response.Hits[0].CreatedAt.Equal(ts) || len(response.Hits[0].TitleHighlights) != 1 || !response.Hits[1].CreatedAt.IsZero() {
		t.Fatalf("hits = %+v", response.Hits)
	}
	if len(response.Facets) != 1 || response.Facets[0].Values[0].Count != 2 || len(response.Histogram) != 1 {
		t.Fatalf("aggregates = %+v %+v", response.Facets, response.Histogram)
	}
	if f := response.Interpreted.Filters; len(f) != 1 || f[0].Field != "kind" || !f[0].Negated {
		t.Fatalf("filters = %+v", f)
	}
}

func TestResolveSearchProjectSlugMatchesDisplayName(t *testing.T) {
	f := newSearchFixture(t)
	p := f.addProject(t, "Gamma Ray", "gamma-root", nil)
	resolve := f.handler.resolveSearchProjectSlug(t.Context())
	if id, err := resolve("gamma ray"); err != nil || id != p.ID {
		t.Fatalf("resolve display name = %q, %v", id, err)
	}
	if _, err := resolve("  "); err == nil {
		t.Fatal("blank slug resolved")
	}
	if got := searchProjectDisplayName(nil); got != "" {
		t.Fatalf("nil display name = %q", got)
	}
	if got := searchProjectDisplayName(&project.Project{ID: "bare"}); got != "bare" {
		t.Fatalf("rootless display name = %q", got)
	}
	if got := searchProjectSlug(project.Project{ID: "bare"}); got != "" {
		t.Fatalf("rootless slug = %q", got)
	}
}

func TestSearchPageCachePrunesExpiredGenerations(t *testing.T) {
	cache := newSearchPageCache()
	generation := cache.put("scope", completeSearchResponse("hit"))
	entry := cache.entries[generation]
	entry.expiresAt = time.Now().Add(-time.Second)
	cache.entries[generation] = entry
	if _, ok := cache.get(generation, "scope"); ok || len(cache.entries) != 0 {
		t.Fatalf("expired generation survived: %d entries", len(cache.entries))
	}
}
