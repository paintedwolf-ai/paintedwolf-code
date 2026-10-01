package summarize

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
)

func TestRankStructurePrefersSymbolsOverCommentHeads(t *testing.T) {
	structure := []StructureCandidate{
		{RelPath: "styles/a.css", Kind: StructureKindFile, Head: "/* theme tokens */\n:root {}\n", StartLine: 1},
		{RelPath: "styles/b.css", Kind: StructureKindFile, Head: "// layout\n.box {}\n", StartLine: 1},
		{RelPath: "App.tsx", Kind: StructureKindFile, Head: "export function App() {}\n", StartLine: 1,
			Symbols: []StructureSymbol{{Kind: "function", Name: "App", Line: 1}}},
		{RelPath: "api/client.ts", Kind: StructureKindFile, Head: "export const client = {}\n", StartLine: 1,
			Symbols: []StructureSymbol{{Kind: "const", Name: "client", Line: 1}}},
	}
	ranked := rankStructureCandidates(context.Background(), noRerank, "map the app", structure)
	if ranked[0].RelPath != "App.tsx" && ranked[0].RelPath != "api/client.ts" {
		t.Fatalf("first = %s, want a symbol-bearing file", ranked[0].RelPath)
	}
	for i, s := range ranked[:2] {
		if len(s.Symbols) == 0 {
			t.Fatalf("top file %d %s has no symbols", i, s.RelPath)
		}
	}
}

// enginePolicies enables one site with a generous budget.
func enginePolicies(site decide.Site, weight float64) decide.Policies {
	return decide.Policies{site: decide.RerankPolicy{Enabled: true, Deadline: 1e9, MaxCandidates: 64, Chunk: 16, Weight: weight}}
}

func TestDefinitionsFollowTheEngineWhenItsSiteIsEnabled(t *testing.T) {
	defs := []DefinitionItem{
		{RelPath: "pkg/a.go", Kind: "func", Name: "Open", Line: 10, Head: "package a\n"},
		{RelPath: "pkg/b.go", Kind: "func", Name: "Close", Line: 12, Head: "package b\n"},
		{RelPath: "pkg/c.go", Kind: "func", Name: "Flush", Line: 14, Head: "package c\n", Signature: "func (w *Writer) Flush() error {"},
	}
	fake := &decidetest.Fake{}
	rr := decide.Reranker{Decider: fake, Policies: enginePolicies(decide.SiteSummarizeDefinitions, 2)}
	// The engine rates Flush a direct match and the rest irrelevant; the task
	// shares no token with any name, so lexical order is salience-flat.
	fake.Scores = []float64{0, 0, 4}
	ranked := rankDefinitions(context.Background(), rr, "where is the buffer written out", defs)
	if ranked[0].Name != "Flush" {
		t.Fatalf("engine relevance ignored: %+v", ranked)
	}
	if len(fake.Ranks) != 1 || len(fake.Ranks[0].Candidates) != 3 {
		t.Fatalf("engine calls = %+v", fake.Ranks)
	}
	if text := fake.Ranks[0].Candidates[2]; text != "File: pkg/c.go\nSymbol: Flush (func)\nfunc (w *Writer) Flush() error {" {
		t.Fatalf("candidate text = %q", text)
	}
	if text := fake.Ranks[0].Candidates[0]; text != "File: pkg/a.go\nSymbol: Open (func)" {
		t.Fatalf("candidate text without a signature = %q", text)
	}
	if got := SignatureLine([]string{"a", "  func B() {  ", "c"}, 2); got != "func B() {" {
		t.Fatalf("SignatureLine = %q", got)
	}
	if got := SignatureLine([]string{"a"}, 5); got != "" {
		t.Fatalf("out-of-range SignatureLine = %q", got)
	}
	lines := []string{"package p", "", "// Flush writes buffered bytes.", "// It blocks.", "#[inline]", "func Flush() {}"}
	if got := LeadingComment(lines, 6); got != "Flush writes buffered bytes. It blocks." {
		t.Fatalf("LeadingComment = %q", got)
	}
	if got := LeadingComment(lines, 1); got != "" {
		t.Fatalf("LeadingComment at file start = %q", got)
	}
	withDoc := DefinitionItem{RelPath: "p.go", Name: "Flush", Kind: "func", Signature: "func Flush() {}", Doc: "Flush writes buffered bytes."}
	if got := DefinitionText(withDoc); got != "File: p.go\nSymbol: Flush (func)\nfunc Flush() {}\nDoc: Flush writes buffered bytes." {
		t.Fatalf("DefinitionText with doc = %q", got)
	}
	// A disabled site never reaches the engine.
	off := decide.Reranker{Decider: fake, Policies: enginePolicies(decide.SiteSummarizeStructure, 2)}
	rankDefinitions(context.Background(), off, "where is the buffer written out", defs)
	if len(fake.Ranks) != 1 {
		t.Fatal("disabled site called the engine")
	}
}

func TestFocusSourceFollowsTheEngineOnTheWindowsSite(t *testing.T) {
	body := "package p\n" + strings.Repeat("// filler\n", 60) + "func Drain() {}\n" + strings.Repeat("// more\n", 60) + "func Fill() {}\n"
	candidate := StructureCandidate{RelPath: "p.go", Kind: StructureKindFile, StartLine: 1, Head: "package p\n", Symbols: []StructureSymbol{
		{Kind: "func", Name: "Drain", Line: 62}, {Kind: "func", Name: "Fill", Line: 123},
	}}
	// No token of the task matches either name; only the engine can choose.
	fake := &decidetest.Fake{Scores: []float64{0, 4}}
	rr := decide.Reranker{Decider: fake, Policies: enginePolicies(decide.SiteSummarizeWindows, 1)}
	focused := FocusSource(context.Background(), rr, "where does the buffer get topped up", candidate, body, 20)
	if focused.StartLine != 123 {
		t.Fatalf("window start = %d, want the engine's pick", focused.StartLine)
	}
	if len(fake.Ranks) != 1 || fake.Ranks[0].Candidates[1] != "File: p.go\nSymbol: Fill (func)" {
		t.Fatalf("engine call = %+v", fake.Ranks)
	}
}

func TestStructureCandidateTextBoundsTheHead(t *testing.T) {
	long := strings.Repeat("é", rerankHeadRunes+50)
	got := structureCandidateText(StructureCandidate{RelPath: "x.go", Language: "go", Head: long})
	if !strings.HasPrefix(got, "File: x.go (go)\n") {
		t.Fatalf("text = %q", got)
	}
	if body := strings.TrimPrefix(got, "File: x.go (go)\n"); len([]rune(body)) != rerankHeadRunes {
		t.Fatalf("head kept %d runes, want %d", len([]rune(body)), rerankHeadRunes)
	}
}
