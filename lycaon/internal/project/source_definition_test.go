package project

import (
	"context"
	"errors"
	"fmt"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	guidancetestsetup.Install()
	gittestsetup.Enable()
	os.Exit(m.Run())
}

func TestResolveDefinitionsOneCandidate(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755))
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	write("lib/resolve.go", "package lib\n\nfunc Resolve() {}\n")
	write("main.go", "package main\n\nfunc main() { Resolve() }\n")

	p, err := CreateWithRoot(context.Background(), NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)

	got, err := ResolveProjectSourceDefinitions(context.Background(), p, SourceDefinitionRequest{
		RootID: p.Roots[0].ID,
		Path:   "main.go",
		Symbol: "Resolve",
		Line:   3,
	}, testDeclarationSearch)
	testutil.FailErr(t, "resolve", err)
	if got.Truncated {
		t.Fatal("unexpected truncated")
	}
	if len(got.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want 1", got.Candidates)
	}
	c := got.Candidates[0]
	if c.Path != "lib/resolve.go" || c.Kind != SourceSymbolKindFunction || c.Line != 3 {
		t.Fatalf("candidate = %+v", c)
	}
}

func TestResolveDefinitionsRankOrderTwoFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755))
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	// Same-directory declarations rank first.
	write("pkg/def.go", "package pkg\n\nfunc Helper() {}\n")
	write("other/def.go", "package other\n\nfunc Helper() {}\n")
	write("pkg/use.go", "package pkg\n\nfunc use() { Helper() }\n")

	p, err := CreateWithRoot(context.Background(), NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)

	got, err := ResolveProjectSourceDefinitions(context.Background(), p, SourceDefinitionRequest{
		RootID: p.Roots[0].ID,
		Path:   "pkg/use.go",
		Symbol: "Helper",
		Line:   3,
	}, testDeclarationSearch)
	testutil.FailErr(t, "resolve", err)
	if len(got.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want 2", got.Candidates)
	}
	if got.Candidates[0].Path != "pkg/def.go" {
		t.Fatalf("rank[0] = %q, want pkg/def.go", got.Candidates[0].Path)
	}
	if got.Candidates[1].Path != "other/def.go" {
		t.Fatalf("rank[1] = %q, want other/def.go", got.Candidates[1].Path)
	}
}

func TestResolveDefinitionsCallOnlyIsZero(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	write("a.go", "package a\n\nfunc use() { External() }\n")
	write("b.go", "package a\n\nfunc other() { External() }\n")

	p, err := CreateWithRoot(context.Background(), NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)

	got, err := ResolveProjectSourceDefinitions(context.Background(), p, SourceDefinitionRequest{
		RootID: p.Roots[0].ID,
		Path:   "a.go",
		Symbol: "External",
		Line:   3,
	}, testDeclarationSearch)
	testutil.FailErr(t, "resolve", err)
	if len(got.Candidates) != 0 {
		t.Fatalf("call-only should yield zero candidates, got %+v", got.Candidates)
	}
}

func TestResolveDefinitionsBuiltinTypeOnlyIsZero(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(
		filepath.Join(dir, "a.go"),
		[]byte("package a\n\nfunc value() string { return \"\" }\n"),
		0o644,
	))
	p, err := CreateWithRoot(context.Background(), NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)

	got, err := ResolveProjectSourceDefinitions(context.Background(), p, SourceDefinitionRequest{
		RootID: p.Roots[0].ID,
		Path:   "a.go",
		Symbol: "string",
		Line:   3,
	}, testDeclarationSearch)
	testutil.FailErr(t, "resolve", err)
	if len(got.Candidates) != 0 {
		t.Fatalf("built-in type reference must not be a declaration, got %+v", got.Candidates)
	}
}

func TestResolveDefinitionsNoGrammarIsZero(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("Resolve is mentioned\n"), 0o644))
	p, err := CreateWithRoot(context.Background(), NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)

	got, err := ResolveProjectSourceDefinitions(context.Background(), p, SourceDefinitionRequest{
		RootID: p.Roots[0].ID,
		Path:   "notes.txt",
		Symbol: "Resolve",
		Line:   1,
	}, testDeclarationSearch)
	testutil.FailErr(t, "resolve", err)
	if len(got.Candidates) != 0 {
		t.Fatalf("no-grammar should yield zero, got %+v", got.Candidates)
	}
}

func TestResolveDefinitionsEmptySymbolNoSearch(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644))
	p, err := CreateWithRoot(context.Background(), NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)
	searchCalled := false
	search := func(context.Context, DeclarationSearchQuery) ([]DeclarationSearchHit, DeclarationCoverage, error) {
		searchCalled = true
		return nil, DeclarationCoverage{}, nil
	}

	got, err := ResolveProjectSourceDefinitions(context.Background(), p, SourceDefinitionRequest{
		RootID: p.Roots[0].ID,
		Path:   "a.go",
		Symbol: "   ",
		Line:   1,
	}, search)
	testutil.FailErr(t, "resolve", err)
	if len(got.Candidates) != 0 {
		t.Fatalf("empty symbol candidates = %+v", got.Candidates)
	}
	if searchCalled {
		t.Fatal("empty symbol ran definition search")
	}
}

func TestResolveDefinitionsRequiresSearch(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644))
	p, err := CreateWithRoot(context.Background(), NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)

	_, err = ResolveProjectSourceDefinitions(context.Background(), p, SourceDefinitionRequest{
		RootID: p.Roots[0].ID,
		Path:   "a.go",
		Symbol: "Name",
		Line:   1,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "definition search is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveDefinitionsReturnsSearchError(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644))
	p, err := CreateWithRoot(context.Background(), NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)
	want := errors.New("search failed")
	search := func(context.Context, DeclarationSearchQuery) ([]DeclarationSearchHit, DeclarationCoverage, error) {
		return nil, DeclarationCoverage{}, want
	}

	_, err = ResolveProjectSourceDefinitions(context.Background(), p, SourceDefinitionRequest{
		RootID: p.Roots[0].ID,
		Path:   "a.go",
		Symbol: "Name",
		Line:   1,
	}, search)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestResolveDefinitionsMentionsAreNotDeclarations(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755))
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	write("lib/resolve.go", "package lib\n\nfunc Resolve() {}\n")
	for i := 0; i < 30; i++ {
		write(fmt.Sprintf("calls/c%02d.go", i), fmt.Sprintf("package calls\n\nfunc f%d() { Resolve() }\n", i))
	}

	p, err := CreateWithRoot(context.Background(), NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)

	got, err := ResolveProjectSourceDefinitions(context.Background(), p, SourceDefinitionRequest{
		RootID: p.Roots[0].ID,
		Path:   "calls/c00.go",
		Symbol: "Resolve",
		Line:   3,
	}, testDeclarationSearch)
	testutil.FailErr(t, "resolve", err)
	if len(got.Candidates) != 1 {
		t.Fatalf("want exactly one declaration among 30 mention files, got %+v", got.Candidates)
	}
	if got.Candidates[0].Path != "lib/resolve.go" {
		t.Fatalf("path = %q", got.Candidates[0].Path)
	}
}

func TestResolveDefinitionsLanguageFixtures(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		symbol  string
		content string
	}{
		{"go", "sample.go", "Hello", "package sample\n\nfunc Hello() {}\n\nfunc use() { Hello() }\n"},
		{"ts", "sample.ts", "greetTs", "export function greetTs(): void {}\ngreetTs();\n"},
		{"py", "sample.py", "greet_py", "def greet_py():\n    return 1\n\ngreet_py()\n"},
		{"rs", "sample.rs", "greet_rs", "fn greet_rs() {}\n\nfn use_it() { greet_rs(); }\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, tc.path), []byte(tc.content), 0o644))
			p, err := CreateWithRoot(context.Background(), NewMemoryRegistry(), dir)
			testutil.FailErr(t, "create project", err)
			got, err := ResolveProjectSourceDefinitions(context.Background(), p, SourceDefinitionRequest{
				RootID: p.Roots[0].ID,
				Path:   tc.path,
				Symbol: tc.symbol,
				Line:   2,
			}, testDeclarationSearch)
			testutil.FailErr(t, "resolve", err)
			if len(got.Candidates) != 1 {
				t.Fatalf("candidates = %+v, want 1 for %s", got.Candidates, tc.path)
			}
			if got.Candidates[0].Path != tc.path {
				t.Fatalf("path = %q", got.Candidates[0].Path)
			}
		})
	}
}

func TestResolveDefinitionsFileCapTruncates(t *testing.T) {
	dir := t.TempDir()
	// Fifty matches exercise the extraction cap and truncation signal.
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("f%02d.go", i)
		body := "package p\n\nfunc UniqueSym() {}\n"
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	p, err := CreateWithRoot(context.Background(), NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)

	got, err := ResolveProjectSourceDefinitions(context.Background(), p, SourceDefinitionRequest{
		RootID: p.Roots[0].ID,
		Path:   "f00.go",
		Symbol: "UniqueSym",
		Line:   3,
	}, testDeclarationSearch)
	testutil.FailErr(t, "resolve", err)
	if !got.Truncated {
		t.Fatal("expected truncated when >40 distinct mention files")
	}
	if len(got.Candidates) != DefinitionFileCap {
		t.Fatalf("candidates = %d, want %d", len(got.Candidates), DefinitionFileCap)
	}
}

func TestResolveDefinitionsThreeHundredMentionsCap(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 300; i++ {
		name := fmt.Sprintf("m%03d.go", i)
		body := "package p\n\nfunc Resolve() {}\n"
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	p, err := CreateWithRoot(context.Background(), NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)

	got, err := ResolveProjectSourceDefinitions(context.Background(), p, SourceDefinitionRequest{
		RootID: p.Roots[0].ID,
		Path:   "m001.go",
		Symbol: "Resolve",
		Line:   3,
	}, testDeclarationSearch)
	testutil.FailErr(t, "resolve", err)
	if !got.Truncated {
		t.Fatal("expected truncated on 300-mention fixture")
	}
	if len(got.Candidates) != DefinitionFileCap {
		t.Fatalf("candidates = %d, want %d", len(got.Candidates), DefinitionFileCap)
	}
}

func TestResolveDefinitionsParallelExtractionIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	const files = 24
	for i := 0; i < files; i++ {
		name := fmt.Sprintf("d%02d.go", i)
		body := "package p\n\nfunc Shared() {}\n"
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	p, err := CreateWithRoot(context.Background(), NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)

	resolve := func() []string {
		got, err := ResolveProjectSourceDefinitions(context.Background(), p, SourceDefinitionRequest{
			RootID: p.Roots[0].ID,
			Path:   "d00.go",
			Symbol: "Shared",
			Line:   3,
		}, testDeclarationSearch)
		testutil.FailErr(t, "resolve", err)
		paths := make([]string, 0, len(got.Candidates))
		for _, c := range got.Candidates {
			paths = append(paths, c.Path)
		}
		return paths
	}

	first := resolve()
	if len(first) != files {
		t.Fatalf("candidates = %d, want %d", len(first), files)
	}
	if first[0] != "d00.go" {
		t.Fatalf("rank[0] = %q, want origin file d00.go", first[0])
	}
	second := resolve()
	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Fatalf("ranked order changed between runs:\n%v\n%v", first, second)
	}
}

func TestDefinitionRankSameFileFirst(t *testing.T) {
	origin := "pkg/a.go"
	a := SourceDefinitionCandidate{Path: "pkg/a.go", Line: 10}
	b := SourceDefinitionCandidate{Path: "pkg/b.go", Line: 1}
	if !definitionRankLess(a, b, origin, 9) {
		t.Fatal("same file should rank first")
	}
	if definitionRankLess(b, a, origin, 9) {
		t.Fatal("other file should not beat same file")
	}
}

func TestDefinitionRankNearestSameFileFirst(t *testing.T) {
	origin := "pkg/a.go"
	near := SourceDefinitionCandidate{Path: origin, Line: 90}
	far := SourceDefinitionCandidate{Path: origin, Line: 10}
	if !definitionRankLess(near, far, origin, 100) {
		t.Fatal("nearest same-file declaration should rank first")
	}
}

func TestNoIndexGuardContract(t *testing.T) {
	// Definition lookup stays live and schema-free.
	schema, err := os.ReadFile(filepath.Join("..", "db", "schema.sql"))
	testutil.FailErr(t, "read schema", err)
	lower := strings.ToLower(string(schema))
	for _, banned := range []string{"symbol_index", "source_definition", "definition_cache"} {
		if strings.Contains(lower, banned) {
			t.Fatalf("schema must not contain %q", banned)
		}
	}
}
