package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// symbolSearchProject writes files into a new project's root on a SQL-backed server.
func symbolSearchProject(t *testing.T, files map[string]string) (*Server, *project.Project) {
	t.Helper()
	srv := newTestServerWithWorkflows(t)
	dir := t.TempDir()
	for name, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(name))
		testutil.FailErr(t, "mkdir "+name, os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write "+name, os.WriteFile(abs, []byte(content), 0o644))
	}
	p, err := project.CreateWithRoot(t.Context(), srv.projectRegistry, dir)
	testutil.FailErr(t, "create project", err)
	return srv, p
}

// settledSearch posts a complete-budget search until the answer has no
// coverage issues, so a cold catalog cannot stand in for a real answer. It
// polls within the host's request limit and honours Retry-After.
func settledSearch(t *testing.T, srv *Server, p *project.Project, body map[string]any) wire.SearchResponse {
	t.Helper()
	body["origin_project_id"] = p.ID
	body["budget"] = "complete"
	raw, err := json.Marshal(body)
	testutil.FailErr(t, "encode search", err)
	var resp wire.SearchResponse
	deadline := time.Now().Add(testutil.Timeout(15 * time.Second))
	for {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, newAuthedRequest(http.MethodPost, "/v1/search", strings.NewReader(string(raw))))
		wait := searchSettlePoll
		switch w.Code {
		case http.StatusOK:
			testutil.FailErr(t, "decode search", json.Unmarshal(w.Body.Bytes(), &resp))
			if len(resp.Issues) == 0 {
				return resp
			}
		case http.StatusTooManyRequests:
			if seconds, err := strconv.Atoi(w.Header().Get("Retry-After")); err == nil {
				wait = time.Duration(seconds) * time.Second
			}
		default:
			t.Fatalf("search = %d %s", w.Code, w.Body.String())
		}
		if time.Now().Add(wait).After(deadline) {
			t.Fatalf("search never settled: issues %+v", resp.Issues)
		}
		time.Sleep(wait)
	}
}

// searchSettlePoll keeps settledSearch under the host's write limit.
const searchSettlePoll = 200 * time.Millisecond

func hitsOfKind(resp wire.SearchResponse, kind string) []wire.SearchHit {
	var out []wire.SearchHit
	for _, hit := range resp.Hits {
		if hit.HitKind == kind {
			out = append(out, hit)
		}
	}
	return out
}

func TestSearchFindsAbbreviatedDeclarationOutsideDependencies(t *testing.T) {
	srv, p := symbolSearchProject(t, map[string]string{
		"internal/config/parse.go":   "package config\n\nfunc ParseConfig() {}\n",
		"main.go":                    "package main\n\nfunc main() { config.ParseConfig() }\n",
		"node_modules/lib/config.js": "function ParseConfig() {}\n",
	})
	resp := settledSearch(t, srv, p, map[string]any{"query": "kind:symbol parsecfg"})

	if len(resp.Hits) != 1 {
		t.Fatalf("hits = %+v, want one declaration", resp.Hits)
	}
	got := resp.Hits[0]
	if got.HitKind != search.HitKindSymbol || got.Source != search.SourceCode || got.ProjectID != p.ID || got.RootID != p.Roots[0].ID {
		t.Fatalf("hit identity = %+v", got)
	}
	if got.Title != "ParseConfig" || got.Context != "internal/config/parse.go:3" || got.Path != "internal/config/parse.go" || got.Line != 3 {
		t.Fatalf("hit location = %+v", got)
	}
	if got.SymbolKind != wire.SourceSymbolKindFunction {
		t.Fatalf("symbol kind = %q, want function", got.SymbolKind)
	}
	want := []wire.SourceSearchHighlight{{Start: 0, End: 6}, {Start: 8, End: 9}, {Start: 10, End: 11}}
	if !reflect.DeepEqual(got.TitleHighlights, want) {
		t.Fatalf("title highlights = %+v, want %+v", got.TitleHighlights, want)
	}
}

func TestSearchAnswersOneTermWithDeclarationsAndContent(t *testing.T) {
	srv, p := symbolSearchProject(t, map[string]string{
		"internal/config/parse.go": "package config\n\nfunc ParseConfig() {}\n",
		"main.go":                  "package main\n\nfunc main() { config.ParseConfig() }\n",
	})
	resp := settledSearch(t, srv, p, map[string]any{"query": "ParseConfig"})

	symbols := hitsOfKind(resp, search.HitKindSymbol)
	if len(symbols) != 1 || symbols[0].Title != "ParseConfig" {
		t.Fatalf("symbol hits = %+v, want the declaration", symbols)
	}
	if len(hitsOfKind(resp, search.HitKindCode)) == 0 {
		t.Fatalf("hits = %+v, want the content lines too", resp.Hits)
	}
	if resp.Hits[0].HitKind != search.HitKindSymbol {
		t.Fatalf("first hit = %+v, want the exact declaration to rank first", resp.Hits[0])
	}
	facetKinds := map[string]int{}
	for _, facet := range resp.Facets {
		if facet.Key == "kind" {
			for _, value := range facet.Values {
				facetKinds[value.Value] = value.Count
			}
		}
	}
	if facetKinds[search.HitKindSymbol] != 1 {
		t.Fatalf("kind facet = %v, want one symbol", facetKinds)
	}
}

func TestSearchDeclarationsFollowPathFilters(t *testing.T) {
	srv, p := symbolSearchProject(t, map[string]string{
		"internal/config/parse.go": "package config\n\nfunc Resolve() {}\n",
		"cmd/tool/resolve.go":      "package main\n\nfunc Resolve() {}\n",
	})
	inside := settledSearch(t, srv, p, map[string]any{"query": "kind:symbol path:internal Resolve"})
	if len(inside.Hits) != 1 || inside.Hits[0].Path != "internal/config/parse.go" {
		t.Fatalf("path-filtered hits = %+v", inside.Hits)
	}
	outside := settledSearch(t, srv, p, map[string]any{"query": "kind:symbol NOT path:internal Resolve"})
	if len(outside.Hits) != 1 || outside.Hits[0].Path != "cmd/tool/resolve.go" {
		t.Fatalf("negated path hits = %+v", outside.Hits)
	}
	globbed := settledSearch(t, srv, p, map[string]any{"query": "kind:symbol Resolve", "exclude": []string{"cmd/**"}})
	if len(globbed.Hits) != 1 || globbed.Hits[0].Path != "internal/config/parse.go" {
		t.Fatalf("exclude-glob hits = %+v", globbed.Hits)
	}
}

func TestSearchNamesDeclarationsOnlyForOneLiteralTerm(t *testing.T) {
	srv, p := symbolSearchProject(t, map[string]string{
		"parse.go": "package config\n\n// ParseConfig parses config.\nfunc ParseConfig() {}\n",
	})
	for _, body := range []map[string]any{
		{"query": "ParseConfig parses"},
		{"query": "Parse.*", "regex": true},
		{"query": "P"},
	} {
		resp := settledSearch(t, srv, p, body)
		if symbols := hitsOfKind(resp, search.HitKindSymbol); len(symbols) != 0 {
			t.Fatalf("search %v symbol hits = %+v, want none", body, symbols)
		}
	}
	exact := settledSearch(t, srv, p, map[string]any{"query": "kind:symbol parse", "whole_word": true})
	if len(exact.Hits) != 0 {
		t.Fatalf("whole-word hits = %+v, want no partial names", exact.Hits)
	}
	cased := settledSearch(t, srv, p, map[string]any{"query": "kind:symbol parseConfig", "case_sensitive": true})
	if len(cased.Hits) != 0 {
		t.Fatalf("case-sensitive hits = %+v, want none for a different spelling", cased.Hits)
	}
}
