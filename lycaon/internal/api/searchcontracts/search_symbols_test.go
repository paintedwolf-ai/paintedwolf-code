package searchcontracts

import (
	"reflect"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/search"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSearchFindsAbbreviatedDeclarationOutsideDependencies(t *testing.T) {
	srv, p := contractfixture.SymbolSearchProject(t, map[string]string{
		"internal/config/parse.go":   "package config\n\nfunc ParseConfig() {}\n",
		"main.go":                    "package main\n\nfunc main() { config.ParseConfig() }\n",
		"node_modules/lib/config.js": "function ParseConfig() {}\n",
	})
	resp := contractfixture.SettledSearch(t, srv, p, map[string]any{"query": "kind:symbol parsecfg"})

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
	srv, p := contractfixture.SymbolSearchProject(t, map[string]string{
		"internal/config/parse.go": "package config\n\nfunc ParseConfig() {}\n",
		"main.go":                  "package main\n\nfunc main() { config.ParseConfig() }\n",
	})
	resp := contractfixture.SettledSearch(t, srv, p, map[string]any{"query": "ParseConfig"})

	symbols := contractfixture.HitsOfKind(resp, search.HitKindSymbol)
	if len(symbols) != 1 || symbols[0].Title != "ParseConfig" {
		t.Fatalf("symbol hits = %+v, want the declaration", symbols)
	}
	if len(contractfixture.HitsOfKind(resp, search.HitKindCode)) == 0 {
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
	srv, p := contractfixture.SymbolSearchProject(t, map[string]string{
		"internal/config/parse.go": "package config\n\nfunc Resolve() {}\n",
		"cmd/tool/resolve.go":      "package main\n\nfunc Resolve() {}\n",
	})
	inside := contractfixture.SettledSearch(t, srv, p, map[string]any{"query": "kind:symbol path:internal Resolve"})
	if len(inside.Hits) != 1 || inside.Hits[0].Path != "internal/config/parse.go" {
		t.Fatalf("path-filtered hits = %+v", inside.Hits)
	}
	outside := contractfixture.SettledSearch(t, srv, p, map[string]any{"query": "kind:symbol NOT path:internal Resolve"})
	if len(outside.Hits) != 1 || outside.Hits[0].Path != "cmd/tool/resolve.go" {
		t.Fatalf("negated path hits = %+v", outside.Hits)
	}
	globbed := contractfixture.SettledSearch(t, srv, p, map[string]any{"query": "kind:symbol Resolve", "exclude": []string{"cmd/**"}})
	if len(globbed.Hits) != 1 || globbed.Hits[0].Path != "internal/config/parse.go" {
		t.Fatalf("exclude-glob hits = %+v", globbed.Hits)
	}
}

func TestSearchNamesDeclarationsOnlyForOneLiteralTerm(t *testing.T) {
	srv, p := contractfixture.SymbolSearchProject(t, map[string]string{
		"parse.go": "package config\n\n// ParseConfig parses config.\nfunc ParseConfig() {}\n",
	})
	for _, body := range []map[string]any{
		{"query": "ParseConfig parses"},
		{"query": "Parse.*", "regex": true},
		{"query": "P"},
	} {
		resp := contractfixture.SettledSearch(t, srv, p, body)
		if symbols := contractfixture.HitsOfKind(resp, search.HitKindSymbol); len(symbols) != 0 {
			t.Fatalf("search %v symbol hits = %+v, want none", body, symbols)
		}
	}
	exact := contractfixture.SettledSearch(t, srv, p, map[string]any{"query": "kind:symbol parse", "whole_word": true})
	if len(exact.Hits) != 0 {
		t.Fatalf("whole-word hits = %+v, want no partial names", exact.Hits)
	}
	cased := contractfixture.SettledSearch(t, srv, p, map[string]any{"query": "kind:symbol parseConfig", "case_sensitive": true})
	if len(cased.Hits) != 0 {
		t.Fatalf("case-sensitive hits = %+v, want none for a different spelling", cased.Hits)
	}
}
