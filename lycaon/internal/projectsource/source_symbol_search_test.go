package projectsource

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func writeSymbolFixture(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(name))
		testutil.FailErr(t, "mkdir "+name, os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write "+name, os.WriteFile(abs, []byte(content), 0o644))
	}
}

func symbolSearchFixture(t *testing.T, files map[string]string) *Project {
	t.Helper()
	dir := t.TempDir()
	writeSymbolFixture(t, dir, files)
	p := fixtureProject(dir)
	return p
}

// testSymbolWall keeps the request clock from cutting passes on a loaded host;
// discovery timing is the executor's concern.
const testSymbolWall = time.Minute

func searchSymbols(t *testing.T, p *Project, req SourceSymbolSearchRequest) SourceSymbolSearchResult {
	t.Helper()
	req.Wall, req.AbbreviationWall = testSymbolWall, testSymbolWall
	result, err := SearchProjectSourceSymbols(context.Background(), p, req, testDeclarationSearch)
	testutil.FailErr(t, "search symbols "+req.Query, err)
	return result
}

func symbolNames(matches []SourceSymbolMatch) []string {
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, fmt.Sprintf("%s %s %s:%d", m.Name, m.Kind, m.Path, m.Line))
	}
	return out
}

func TestSymbolSearchRanksTiersBeforeKindsAndPaths(t *testing.T) {
	p := symbolSearchFixture(t, map[string]string{
		"names.go": "package p\n\nfunc xcfgx() {}\n\nfunc LoadCfg() {}\n\nfunc CfgLoader() {}\n\nfunc Cfg() {}\n\nfunc cfg() {}\n\nfunc CacheFileGroup() {}\n",
	})
	got := searchSymbols(t, p, SourceSymbolSearchRequest{Query: "cfg"})
	want := []string{
		"cfg function names.go:11",
		"Cfg function names.go:9",
		"CfgLoader function names.go:7",
		"LoadCfg function names.go:5",
		"CacheFileGroup function names.go:13",
		"xcfgx function names.go:3",
	}
	if names := symbolNames(got.Symbols); !reflect.DeepEqual(names, want) {
		t.Fatalf("ranked = %q\nwant     %q", names, want)
	}
	// Skipping abbreviations after an exact name is not a coverage gap.
	if got.Incomplete || got.Limited {
		t.Fatalf("incomplete %v limited %v; want an exact-name search to be complete", got.Incomplete, got.Limited)
	}
	highlights := map[string][]SourceTextRange{}
	for _, m := range got.Symbols {
		highlights[m.Name] = m.Highlights
	}
	for name, want := range map[string][]SourceTextRange{
		"LoadCfg":        {{4, 7}},
		"CacheFileGroup": {{0, 1}, {5, 6}, {9, 10}},
		"xcfgx":          {{1, 4}},
	} {
		if !reflect.DeepEqual(highlights[name], want) {
			t.Errorf("%s highlights = %v, want %v", name, highlights[name], want)
		}
	}
}

func TestSymbolSearchDiscoversAbbreviationsWithoutTheTypedText(t *testing.T) {
	p := symbolSearchFixture(t, map[string]string{
		"loader.go": "package p\n\nfunc LoadCfg() {}\n",
		// No "cfg" substring: only the abbreviation pass can find this file.
		"hump.go": "package p\n\nfunc CacheFileGroup() {}\n",
	})
	got := searchSymbols(t, p, SourceSymbolSearchRequest{Query: "cfg"})
	want := []string{"LoadCfg function loader.go:3", "CacheFileGroup function hump.go:3"}
	if names := symbolNames(got.Symbols); !reflect.DeepEqual(names, want) || got.Incomplete || got.Limited {
		t.Fatalf("matches = %q incomplete %v limited %v, want %q complete", names, got.Incomplete, got.Limited, want)
	}
}

func TestSymbolSearchIgnoresCaseAndOrdersKindsThenDepth(t *testing.T) {
	p := symbolSearchFixture(t, map[string]string{
		"a/deep/parse.go": "package deep\n\nfunc ParseConfig() {}\n",
		"config.go": "package p\n\nconst ParseConfigLimit = 1\n\nfunc parseConfigFile() {}\n\n" +
			"type S struct{}\n\nfunc (s *S) ReparseConfig() {}\n\ntype ParseConfig struct{}\n",
		"README.md": "# ParseConfig\n\nParseConfig reads settings.\n",
	})
	got := searchSymbols(t, p, SourceSymbolSearchRequest{Query: "PARSECONFIG"})
	names := symbolNames(got.Symbols)
	if len(names) != 5 {
		t.Fatalf("matches = %q, want five declarations and no heading", names)
	}
	exactType, exactFunc := names[0], names[1]
	if !strings.HasPrefix(exactType, "ParseConfig ") || !strings.HasSuffix(exactType, "config.go:11") ||
		exactFunc != "ParseConfig function a/deep/parse.go:3" {
		t.Fatalf("exact matches = %q, want the type before the deeper function", names[:2])
	}
	if names[2] != "parseConfigFile function config.go:5" || names[3] != "ParseConfigLimit constant config.go:3" {
		t.Fatalf("prefix matches = %q, want function before constant", names[2:4])
	}
	if names[4] != "ReparseConfig method config.go:9" {
		t.Fatalf("substring match = %q", names[4])
	}
}

func TestSymbolSearchSpansRootsAndHonorsRootID(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	writeSymbolFixture(t, first, map[string]string{"one.go": "package one\n\nfunc SharedName() {}\n"})
	writeSymbolFixture(t, second, map[string]string{"two.go": "package two\n\nfunc SharedName() {}\n"})
	p := fixtureProject(first, second)
	rootHolding := func(file string) string {
		for _, root := range p.Roots {
			if _, err := os.Stat(filepath.Join(root.Path, file)); err == nil {
				return root.ID
			}
		}
		return ""
	}
	oneRoot, twoRoot := rootHolding("one.go"), rootHolding("two.go")
	if oneRoot == "" || twoRoot == "" || oneRoot == twoRoot {
		t.Fatalf("roots = %+v, want one fixture file per root", p.Roots)
	}

	got := searchSymbols(t, p, SourceSymbolSearchRequest{Query: "SharedName"})
	roots := map[string]string{}
	for _, m := range got.Symbols {
		roots[m.Path] = m.RootID
	}
	if len(got.Symbols) != 2 || roots["one.go"] != oneRoot || roots["two.go"] != twoRoot {
		t.Fatalf("matches = %+v, want one declaration per root", got.Symbols)
	}

	scoped := searchSymbols(t, p, SourceSymbolSearchRequest{Query: "SharedName", RootIDs: []string{twoRoot}})
	if len(scoped.Symbols) != 1 || scoped.Symbols[0].Path != "two.go" || scoped.Symbols[0].RootID != twoRoot {
		t.Fatalf("root-scoped matches = %+v", scoped.Symbols)
	}

	_, err = SearchProjectSourceSymbols(context.Background(), p, SourceSymbolSearchRequest{Query: "SharedName", RootIDs: []string{"missing"}, Wall: testSymbolWall}, testDeclarationSearch)
	if !errors.Is(err, ErrSourceNoRoot) {
		t.Fatalf("unknown root error = %v, want ErrSourceNoRoot", err)
	}
}

func TestSymbolSearchExcludesDependencyDirectories(t *testing.T) {
	p := symbolSearchFixture(t, map[string]string{
		"src/app.js":                 "function sharedHelper() {}\n",
		"node_modules/lib/index.js":  "function sharedHelper() {}\n",
		"src/vendor/copied/index.js": "function sharedHelper() {}\n",
	})
	got := searchSymbols(t, p, SourceSymbolSearchRequest{Query: "sharedHelper", ExcludeDirs: []string{"node_modules", "vendor"}})
	if names := symbolNames(got.Symbols); !reflect.DeepEqual(names, []string{"sharedHelper function src/app.js:1"}) {
		t.Fatalf("matches = %q, want only the source declaration", names)
	}
}

func TestSymbolSearchFileBudgetAndLimitTruncate(t *testing.T) {
	files := map[string]string{}
	for i := range SymbolSearchFileCap + 12 {
		files[fmt.Sprintf("f%03d.go", i)] = "package p\n\nfunc CapTarget() {}\n"
	}
	p := symbolSearchFixture(t, files)

	got := searchSymbols(t, p, SourceSymbolSearchRequest{Query: "CapTarget", Limit: SourceSymbolSearchMaxLimit})
	if !got.Incomplete || len(got.Symbols) != SymbolSearchFileCap {
		t.Fatalf("file budget = %d symbols, incomplete %v; want %d incomplete", len(got.Symbols), got.Incomplete, SymbolSearchFileCap)
	}

	limited := searchSymbols(t, p, SourceSymbolSearchRequest{Query: "CapTarget", Limit: 3})
	if !limited.Limited || len(limited.Symbols) != 3 {
		t.Fatalf("limit = %d symbols, limited %v; want 3 limited", len(limited.Symbols), limited.Limited)
	}
	if limited.Symbols[0].Path != "f000.go" {
		t.Fatalf("first match = %s, want path order within a tier", limited.Symbols[0].Path)
	}
}

func TestSymbolSearchShortQueryRunsNoSearch(t *testing.T) {
	p := symbolSearchFixture(t, map[string]string{"a.go": "package a\n\nfunc A() {}\n"})
	called := false
	search := func(context.Context, DeclarationSearchQuery) ([]DeclarationSearchHit, bool, error) {
		called = true
		return nil, false, nil
	}
	for _, query := range []string{"", " ", "A", " é "} {
		got, err := SearchProjectSourceSymbols(context.Background(), p, SourceSymbolSearchRequest{Query: query}, search)
		testutil.FailErr(t, "short query", err)
		if got.Symbols == nil || len(got.Symbols) != 0 || got.Incomplete || got.Limited {
			t.Fatalf("query %q = %+v, want an empty complete list", query, got)
		}
	}
	if called {
		t.Fatal("short query ran discovery")
	}
}

func TestSymbolSearchSkipsAbbreviationsBehindAFullPage(t *testing.T) {
	p := symbolSearchFixture(t, map[string]string{
		"a.go":    "package p\n\nfunc CfgOne() {}\n\nfunc CfgTwo() {}\n",
		"hump.go": "package p\n\nfunc CacheFileGroup() {}\n",
	})
	var patterns []DeclarationMatch
	search := func(ctx context.Context, query DeclarationSearchQuery) ([]DeclarationSearchHit, bool, error) {
		patterns = append(patterns, query.Match)
		return testDeclarationSearch(ctx, query)
	}
	got, err := SearchProjectSourceSymbols(context.Background(), p, SourceSymbolSearchRequest{Query: "cfg", Limit: 2, Wall: testSymbolWall, AbbreviationWall: testSymbolWall}, search)
	testutil.FailErr(t, "search", err)
	if names := symbolNames(got.Symbols); len(names) != 2 || !got.Limited || got.Incomplete {
		t.Fatalf("matches = %q limited %v incomplete %v, want the two prefix matches, limited", names, got.Limited, got.Incomplete)
	}
	if !reflect.DeepEqual(patterns, []DeclarationMatch{DeclarationMatchSubstring}) {
		t.Fatalf("passes = %v, want only the substring pass", patterns)
	}
}

func TestSymbolSearchReturnsDiscoveryError(t *testing.T) {
	p := symbolSearchFixture(t, map[string]string{"a.go": "package a\n"})
	want := errors.New("search failed")
	search := func(context.Context, DeclarationSearchQuery) ([]DeclarationSearchHit, bool, error) {
		return nil, false, want
	}
	if _, err := SearchProjectSourceSymbols(context.Background(), p, SourceSymbolSearchRequest{Query: "Name"}, search); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestSymbolSearchExactNameSkipsAbbreviations(t *testing.T) {
	p := symbolSearchFixture(t, map[string]string{
		"a.go":    "package p\n\nfunc Cfg() {}\n",
		"hump.go": "package p\n\nfunc CacheFileGroup() {}\n",
	})
	var passes []DeclarationMatch
	search := func(ctx context.Context, query DeclarationSearchQuery) ([]DeclarationSearchHit, bool, error) {
		passes = append(passes, query.Match)
		return testDeclarationSearch(ctx, query)
	}
	got, err := SearchProjectSourceSymbols(context.Background(), p, SourceSymbolSearchRequest{
		Query: "cfg", Wall: testSymbolWall, AbbreviationWall: testSymbolWall,
	}, search)
	testutil.FailErr(t, "search", err)
	if names := symbolNames(got.Symbols); !reflect.DeepEqual(names, []string{"Cfg function a.go:3"}) || got.Incomplete || got.Limited {
		t.Fatalf("matches = %q incomplete %v limited %v, want the exact name, complete", names, got.Incomplete, got.Limited)
	}
	if !reflect.DeepEqual(passes, []DeclarationMatch{DeclarationMatchSubstring}) {
		t.Fatalf("passes = %v, want only the substring pass", passes)
	}
}

func TestSymbolSearchExactKeepsWholeNamesFromOneWholeWordPass(t *testing.T) {
	p := symbolSearchFixture(t, map[string]string{
		"a.go": "package p\n\nfunc Cfg() {}\n\nfunc cfg() {}\n\nfunc CfgLoader() {}\n\nfunc LoadCfg() {}\n",
	})
	var passes []DeclarationMatch
	search := func(ctx context.Context, query DeclarationSearchQuery) ([]DeclarationSearchHit, bool, error) {
		passes = append(passes, query.Match)
		return testDeclarationSearch(ctx, query)
	}
	got, err := SearchProjectSourceSymbols(context.Background(), p, SourceSymbolSearchRequest{
		Query: "cfg", Exact: true, Wall: testSymbolWall, AbbreviationWall: testSymbolWall,
	}, search)
	testutil.FailErr(t, "search", err)
	if names := symbolNames(got.Symbols); !reflect.DeepEqual(names, []string{"cfg function a.go:5", "Cfg function a.go:3"}) {
		t.Fatalf("exact matches = %q, want both whole names, the query's spelling first", names)
	}
	if !reflect.DeepEqual(passes, []DeclarationMatch{DeclarationMatchWholeWord}) {
		t.Fatalf("passes = %v, want one whole-word pass", passes)
	}
	cased, err := SearchProjectSourceSymbols(context.Background(), p, SourceSymbolSearchRequest{
		Query: "Cfg", Exact: true, CaseSensitive: true, Wall: testSymbolWall, AbbreviationWall: testSymbolWall,
	}, testDeclarationSearch)
	testutil.FailErr(t, "case-sensitive exact search", err)
	if names := symbolNames(cased.Symbols); !reflect.DeepEqual(names, []string{"Cfg function a.go:3"}) {
		t.Fatalf("case-sensitive exact matches = %q, want only Cfg", names)
	}
}

func TestSymbolSearchCaseSensitiveKeepsMatchesSpelledAsTyped(t *testing.T) {
	p := symbolSearchFixture(t, map[string]string{
		"a.go": "package p\n\nfunc ParseConfig() {}\n\nfunc parseconfig() {}\n\nfunc ParseCfgFile() {}\n",
	})
	got := searchSymbols(t, p, SourceSymbolSearchRequest{Query: "PC", CaseSensitive: true})
	if names := symbolNames(got.Symbols); !reflect.DeepEqual(names, []string{"ParseConfig function a.go:3", "ParseCfgFile function a.go:7"}) {
		t.Fatalf("case-sensitive abbreviation matches = %q, want the capitalised humps only", names)
	}
	prefix := searchSymbols(t, p, SourceSymbolSearchRequest{Query: "parse", CaseSensitive: true})
	if names := symbolNames(prefix.Symbols); !reflect.DeepEqual(names, []string{"parseconfig function a.go:5"}) {
		t.Fatalf("case-sensitive prefix matches = %q, want only the lowercase name", names)
	}
}
