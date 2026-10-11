package searchadmin

import (
	"net/http/httptest"
	"testing"
)

func TestSearchHTTPExcludedExactNamesCannotSuppressAdmissibleAbbreviations(t *testing.T) {
	f := newSearchFixture(t)
	p := f.addProject(t, "admission", "source", map[string]string{
		"exact.go":        "package p\nfunc PC() {}\n",
		"abbreviation.go": "package p\nfunc ParseConfig() {}\n",
		"other.go":        "package p\nfunc ProcessCache() {}\n",
	})
	server := httptest.NewServer(f.router)
	defer server.Close()
	for _, query := range []string{
		"kind:symbol pc NOT (path:exact.go OR path:other.go)",
		"kind:symbol pc (path:abbreviation.go OR (path:exact.go AND NOT path:exact.go))",
	} {
		for _, budget := range []string{"interactive", "complete"} {
			body := jsonBody(t, map[string]any{"query": query, "origin_project_id": p.ID, "budget": budget})
			result := requestSearchHTTP(t, server, body)
			if len(result.Hits) != 1 || result.Hits[0].Title != "ParseConfig" || len(result.Issues) != 0 {
				t.Fatalf("query=%q budget=%s excluded exact name suppressed reference abbreviation: %+v", query, budget, result)
			}
		}
	}
}
