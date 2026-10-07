package summarize

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBriefingRetainsRelevantSectionAcrossDefinitionDensity(t *testing.T) {
	for _, count := range []int{10, 1000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			source := StructureCandidate{RelPath: "guide.md", Kind: StructureKindFile, StartLine: 1, Head: "# Overview\nGeneral context.\n# Architecture\nThe interface sends requests to the local host.\nThe host owns sessions and tool execution.\n# Installation\nRun setup.\n", Symbols: []StructureSymbol{{Kind: "section", Name: "Overview", Line: 1}, {Kind: "section", Name: "Architecture", Line: 3}, {Kind: "section", Name: "Installation", Line: 6}}}
			lock := StructureCandidate{RelPath: "dependencies.lock", Kind: StructureKindFile, StartLine: 1}
			var body strings.Builder
			for i := range count {
				name := fmt.Sprintf("dependency_%04d", i)
				lock.Symbols = append(lock.Symbols, StructureSymbol{Kind: "key", Name: name, Line: i + 1})
				fmt.Fprintf(&body, "%s=1\n", name)
			}
			lock.Head = body.String()
			pack, _, _ := assembleContextPack(context.Background(), noRerank, "explain architecture", []StructureCandidate{lock, source}, DefaultCaps(), FitEdges{})
			found := false
			for _, window := range pack.Substance {
				if window.Path == "guide.md" && strings.Contains(window.Body, "The host owns sessions") {
					found = true
				}
				if window.Symbol == "Architecture" && strings.Contains(window.Body, "General context") {
					t.Fatalf("section carries unrelated preceding prose: %+v", window)
				}
			}
			if !found {
				t.Fatalf("relevant explanation missing: %+v", pack.Substance)
			}
		})
	}
}

func TestNextActionArgumentsAreExecutableReadRange(t *testing.T) {
	raw, err := json.Marshal(NextAction{Tool: "read", Path: "source.go", Lines: "40-63", Why: "Inspect source detail"})
	testutil.FailErr(t, "encode read action", err)
	var action struct {
		Tool string
		Args struct {
			Path          string
			Offset, Limit int
		}
		Kind string
	}
	testutil.FailErr(t, "decode read action", json.Unmarshal(raw, &action))
	if action.Tool != "read" || action.Args.Path != "source.go" || action.Args.Offset != 40 || action.Args.Limit != 24 || action.Kind != "inspect" {
		t.Fatalf("action=%s", raw)
	}
}

func TestOverlappingSourceWindowsUseMarginalBudget(t *testing.T) {
	caps := DefaultCaps()
	a := PackWindow{Path: "source.go", StartLine: 1, EndLine: 3, Body: numberWindowBody("first\nsecond\nthird", 1)}
	b := PackWindow{Path: "source.go", StartLine: 2, EndLine: 4, Body: numberWindowBody("second\nthird\nfourth", 2)}
	merged := mergeOverlappingWindows([]PackWindow{a, b})
	if len(merged) != 1 || strings.Count(merged[0].Body, "second") != 1 || windowTokens(caps, merged) >= windowTokens(caps, []PackWindow{a, b}) {
		t.Fatalf("overlap double-counted: %+v", merged)
	}
	if merged[0].Body != numberWindowBody("first\nsecond\nthird\nfourth", 1) {
		t.Fatalf("merged body = %q", merged[0].Body)
	}
}

func TestAdjacentSourceWindowsJoinOnRowBoundaries(t *testing.T) {
	a := PackWindow{Path: "source.go", StartLine: 1, EndLine: 2, Body: numberWindowBody("first\nsecond", 1)}
	b := PackWindow{Path: "source.go", StartLine: 3, EndLine: 4, Body: numberWindowBody("third\nfourth", 3)}
	merged := mergeOverlappingWindows([]PackWindow{a, b})
	if len(merged) != 1 || merged[0].EndLine != 4 {
		t.Fatalf("adjacent windows = %+v", merged)
	}
	if merged[0].Body != numberWindowBody("first\nsecond\nthird\nfourth", 1) {
		t.Fatalf("merged body = %q", merged[0].Body)
	}
	for line, want := range map[int]string{1: "first", 2: "second", 3: "third", 4: "fourth"} {
		if text, ok := hostmarker.NumberedLineAt(merged[0].Body, line); !ok || text != want {
			t.Fatalf("line %d = (%q, %v), want %q", line, text, ok, want)
		}
	}
}

func TestBriefingFocusesDefinitionBeyondFileHead(t *testing.T) {
	body := "package principal\n" + strings.Repeat("// unrelated context\n", 500) + "func Subsumes() bool {\n return true\n}\n"
	candidate := StructureCandidate{RelPath: "principal.go", Kind: StructureKindFile, StartLine: 1, Head: "package principal\n", Symbols: []StructureSymbol{{Kind: "func", Name: "Subsumes", Line: 502}}}
	candidate = FocusSource(context.Background(), noRerank, "How does Subsumes work?", candidate, body, 40)
	pack, _, _ := assembleContextPack(context.Background(), noRerank, "How does Subsumes work?", []StructureCandidate{candidate}, DefaultCaps(), FitEdges{})
	if candidate.StartLine != 502 || len(pack.Substance) == 0 || !strings.Contains(pack.Substance[0].Body, "return true") {
		t.Fatalf("late definition source=%+v", pack.Substance)
	}
}

func TestBriefingUsesObservedSourceWhenParserMissesMethod(t *testing.T) {
	body := "OPAQUE_DECLARATIONS\n" + strings.Repeat("// preamble\n", 500) + "bool Principal::Subsumes(Principal* other) {\n return origin == other->origin;\n}\n"
	broken := false
	candidate := StructureCandidate{RelPath: "principal.cpp", Kind: StructureKindFile, Parses: &broken, StartLine: 1, Head: "OPAQUE_DECLARATIONS\n", Symbols: []StructureSymbol{{Kind: "function", Name: "if", Line: 503}}}
	candidate = FocusSource(context.Background(), noRerank, "How does Principal::Subsumes work?", candidate, body, 40)
	pack, _, _ := assembleContextPack(context.Background(), noRerank, "How does Principal::Subsumes work?", []StructureCandidate{candidate}, DefaultCaps(), FitEdges{})
	if len(pack.Substance) == 0 || !strings.Contains(pack.Substance[0].Body, "origin == other->origin") || candidate.StartLine != 502 {
		t.Fatalf("source fallback=%+v", pack.Substance)
	}
}

func TestPatternFollowupsKeepAllSearchesWithinRequestedScopes(t *testing.T) {
	req := Request{Paths: []string{"src/network", "tests/network"}, Pattern: "Route"}
	for _, gathered := range []GatherResult{{MatchCount: 20}, {NextCursorPath: "next"}} {
		actions := ensurePatternCoverageNextAction(nil, req, gathered, 0)
		if len(actions) != 2 {
			t.Fatalf("scoped search actions=%+v", actions)
		}
		for i, action := range actions {
			if action.Tool != "grep" || action.Path != req.Paths[i] || action.Pattern != req.Pattern {
				t.Fatalf("search action widened the requested scope: %+v", action)
			}
		}
	}
}

func TestSourceFocusPrefersQualifiedMethodOverQuestionWords(t *testing.T) {
	body := "OPAQUE_MACROS\n" + strings.Repeat("// other source\n", 500) + "Result Document::Init(Principal* principal) {\n initializeLoaders();\n}\n// How does a document initialize?\nvoid Document::Initialize() {}\n"
	broken := false
	candidate := StructureCandidate{RelPath: "Document.cpp", Kind: StructureKindFile, Parses: &broken, StartLine: 1, Head: "OPAQUE_MACROS\n"}
	candidate = FocusSource(context.Background(), noRerank, "How does Document::Init initialize a document?", candidate, body, 24)
	if candidate.StartLine != 502 || !strings.Contains(candidate.Head, "initializeLoaders") {
		t.Fatalf("qualified method source=%+v", candidate)
	}
}

func TestSourceFocusUsesCodeIdentifiersForBroadQuestion(t *testing.T) {
	body := "OPAQUE_MACROS\n" + strings.Repeat("// surrounding source\n", 40) + "bool BasePrincipal::GetIsContentPrincipal(bool* result) {\n *result = Kind() == eContentPrincipal;\n}\n// Read is used still for legacy principals\nvoid Read() {}\n"
	broken := false
	candidate := StructureCandidate{RelPath: "BasePrincipal.cpp", Kind: StructureKindFile, Parses: &broken, StartLine: 1, Head: "OPAQUE_MACROS\n"}
	candidate = FocusSource(context.Background(), noRerank, "How are principal types organized and used for access checks?", candidate, body, 24)
	if candidate.StartLine < 42 || candidate.StartLine > 43 {
		t.Fatalf("broad question source=%+v", candidate)
	}
}
