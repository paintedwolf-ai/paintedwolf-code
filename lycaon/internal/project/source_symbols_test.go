package project

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceSymbolsReportsIncompleteInsteadOfEmpty(t *testing.T) {
	symbols, err := sourceSymbolsFromAnalysis(fileoutline.Result{
		Diagnostics: &repomap.Diagnostics{SkipReasons: repomap.SkipStats{ParseIncomplete: 1}},
	})
	if !errors.Is(err, repomap.ErrDefinitionIncomplete) || len(symbols) != 0 {
		t.Fatalf("source symbols = %v, %v; want typed incomplete", symbols, err)
	}
	dir := t.TempDir()
	p, err := CreateWithRoot(t.Context(), NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)
	search := func(context.Context, DeclarationSearchQuery) ([]DeclarationSearchHit, bool, error) {
		return []DeclarationSearchHit{{RootID: p.Roots[0].ID, Path: "unavailable.go"}}, false, nil
	}
	result, err := ResolveProjectSourceDefinitions(t.Context(), p, SourceDefinitionRequest{
		RootID: p.Roots[0].ID, Path: "unavailable.go", Symbol: "missing",
	}, search)
	testutil.FailErr(t, "resolve incomplete definitions", err)
	if !result.Truncated || len(result.Candidates) != 0 {
		t.Fatalf("incomplete definition search = %+v; want truncated", result)
	}
}

func TestPatchOutlineProjectsFileBoundariesIntoSourceSymbols(t *testing.T) {
	text := "diff --git a/old.go b/new.go\n--- a/old.go\n+++ b/new.go\n@@ -1 +1 @@\n-old\n+new\n"
	symbols, err := SourceSymbolsForContent(t.Context(), "change.patch", []byte(text))
	testutil.FailErr(t, "project patch outline", err)
	if len(symbols) != 1 || symbols[0].Name != "new.go" || symbols[0].Kind != SourceSymbolKindHeading || symbols[0].Line != 1 {
		t.Fatalf("patch source symbols = %+v", symbols)
	}
}

func TestListProjectSourceSymbolsLockedFixtures(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	write("sample.go", "package sample\n\nconst Max = 3\n\ntype Box struct{ N int }\n\nfunc (b Box) Size() int { return b.N }\n\nfunc Hello() string { return \"hi\" }\n")
	write("sample.ts", "export const Max = 3;\nexport class Box { size(): number { return 1; } }\nexport function hello(): string { return \"hi\"; }\nexport type Point = { x: number; y: number };\n")
	write("sample.py", "MAX = 3\n\nclass Box:\n    def size(self):\n        return 1\n\ndef hello():\n    return \"hi\"\n")
	write("sample.md", "# Title\n\n## Section A\n\ntext\n\n### Nested\n")
	write("sample.txt", "hello world\n")

	p, err := CreateWithRoot(context.Background(), NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)

	cases := []struct {
		path string
		want []SourceSymbol
	}{
		{
			path: "sample.go",
			want: []SourceSymbol{
				{Name: "Max", Kind: SourceSymbolKindConstant, Line: 3},
				{Name: "Box", Kind: SourceSymbolKindType, Line: 5},
				{Name: "Size", Kind: SourceSymbolKindMethod, Line: 7},
				{Name: "Hello", Kind: SourceSymbolKindFunction, Line: 9},
			},
		},
		{
			path: "sample.ts",
			want: []SourceSymbol{
				{Name: "Max", Kind: SourceSymbolKindConstant, Line: 1},
				{Name: "Box", Kind: SourceSymbolKindClass, Line: 2},
				{Name: "size", Kind: SourceSymbolKindMethod, Line: 2},
				{Name: "hello", Kind: SourceSymbolKindFunction, Line: 3},
				{Name: "Point", Kind: SourceSymbolKindType, Line: 4},
			},
		},
		{
			path: "sample.py",
			want: []SourceSymbol{
				{Name: "Box", Kind: SourceSymbolKindClass, Line: 3},
				{Name: "size", Kind: SourceSymbolKindFunction, Line: 4},
				{Name: "hello", Kind: SourceSymbolKindFunction, Line: 7},
			},
		},
		{
			path: "sample.md",
			want: []SourceSymbol{
				{Name: "Title", Kind: SourceSymbolKindHeading, Line: 1},
				{Name: "Section A", Kind: SourceSymbolKindHeading, Line: 3},
				{Name: "Nested", Kind: SourceSymbolKindHeading, Line: 7},
			},
		},
		{
			path: "sample.txt",
			want: []SourceSymbol{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got, err := ListProjectSourceSymbols(context.Background(), p, SourceSymbolsRequest{Path: tc.path})
			testutil.FailErr(t, "list symbols", err)
			if got.Truncated {
				t.Fatal("unexpected truncated")
			}
			if len(got.Symbols) != len(tc.want) {
				t.Fatalf("len = %d, want %d\ngot=%+v", len(got.Symbols), len(tc.want), got.Symbols)
			}
			for i := range tc.want {
				if got.Symbols[i] != tc.want[i] {
					t.Fatalf("symbol[%d] = %+v, want %+v", i, got.Symbols[i], tc.want[i])
				}
			}
		})
	}
}

func TestSourceSymbolsRevisionMatchesAnalyzedBytes(t *testing.T) {
	dir := t.TempDir()
	p, err := CreateWithRoot(context.Background(), NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)
	for _, content := range []string{"# First\n", "# Second\n"} {
		testutil.FailErr(t, "write revision", os.WriteFile(filepath.Join(dir, "outline.md"), []byte(content), 0o644))
		got, err := ListProjectSourceSymbols(context.Background(), p, SourceSymbolsRequest{Path: "outline.md"})
		testutil.FailErr(t, "read outline", err)
		expected := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
		if got.SHA256 != expected {
			t.Fatalf("revision = %q, want %q", got.SHA256, expected)
		}
		if len(got.Symbols) != 1 || got.Symbols[0].Name != strings.TrimSpace(strings.TrimPrefix(content, "# ")) {
			t.Fatalf("outline does not match analyzed revision: %+v", got.Symbols)
		}
	}
}

func TestSourceSymbolKindCoversDefinitionVocabulary(t *testing.T) {
	want := map[string]SourceSymbolKind{
		"function":    SourceSymbolKindFunction,
		"method":      SourceSymbolKindMethod,
		"constructor": SourceSymbolKindMethod,
		"class":       SourceSymbolKindClass,
		"object":      SourceSymbolKindClass,
		"type":        SourceSymbolKindType,
		"interface":   SourceSymbolKindType,
		"module":      SourceSymbolKindType,
		"tag":         SourceSymbolKindType,
		"struct":      SourceSymbolKindType,
		"constant":    SourceSymbolKindConstant,
		"const":       SourceSymbolKindConstant,
		"variable":    SourceSymbolKindConstant,
		"field":       SourceSymbolKindConstant,
		"label":       SourceSymbolKindConstant,
		"number":      SourceSymbolKindConstant,
		"target":      SourceSymbolKindConstant,
		"section":     SourceSymbolKindHeading,
	}
	for definitionKind, wantKind := range want {
		got, ok := sourceSymbolKind(definitionKind)
		if !ok || got != wantKind {
			t.Errorf("sourceSymbolKind(%q) = %q, %v; want %q, true", definitionKind, got, ok, wantKind)
		}
	}
	if got, ok := sourceSymbolKind("reference.call"); ok || got != "" {
		t.Fatalf("reference kind must not become a declaration: got %q, %v", got, ok)
	}
}

func TestSourceSymbolsUsesOutlineFallback(t *testing.T) {
	analysis := fileoutline.Result{Symbols: []fileoutline.Symbol{
		{Kind: "const", Name: "Limit", Line: 2},
		{Kind: "function", Name: "Visible", Line: 4},
	}}
	want := []SourceSymbol{
		{Name: "Limit", Kind: SourceSymbolKindConstant, Line: 2},
		{Name: "Visible", Kind: SourceSymbolKindFunction, Line: 4},
	}
	got, err := sourceSymbolsFromAnalysis(analysis)
	testutil.FailErr(t, "project fallback outline", err)
	if len(got) != len(want) {
		t.Fatalf("symbols = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("symbol[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestListProjectSourceSymbolsTruncates(t *testing.T) {
	dir := t.TempDir()
	var b []byte
	b = append(b, []byte("package p\n")...)
	for i := 0; i < SourceSymbolsMaxEntries+5; i++ {
		b = append(b, []byte("func F"+strconv.Itoa(i)+"() {}\n")...)
	}
	testutil.FailErr(t, "write cap.go", os.WriteFile(filepath.Join(dir, "cap.go"), b, 0o644))
	p, err := CreateWithRoot(context.Background(), NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)
	got, err := ListProjectSourceSymbols(context.Background(), p, SourceSymbolsRequest{Path: "cap.go"})
	testutil.FailErr(t, "list symbols", err)
	if !got.Truncated {
		t.Fatal("want truncated")
	}
	if len(got.Symbols) != SourceSymbolsMaxEntries {
		t.Fatalf("len = %d, want %d", len(got.Symbols), SourceSymbolsMaxEntries)
	}
}

func TestSourceSymbolsRetainsDefinitionsPastRepoMapFileCap(t *testing.T) {
	source := []byte("package p\n\nfunc Visible() {}\n/*" + strings.Repeat("x", 300*1024) + "*/\n")
	symbols, err := SourceSymbolsForContent(context.Background(), "large.go", source)
	testutil.FailErr(t, "analyze large file", err)
	if len(symbols) == 0 || symbols[0].Name != "Visible" {
		t.Fatalf("symbols = %+v, want Visible", symbols)
	}
}
