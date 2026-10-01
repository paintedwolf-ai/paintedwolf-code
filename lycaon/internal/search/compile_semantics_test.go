package search

import (
	"errors"
	"strings"
	"testing"
)

func compileErr(t *testing.T, query string) *ParseError {
	t.Helper()
	_, err := CompileQuery(query, testCompileContext())
	if err == nil {
		t.Fatalf("CompileQuery(%q): expected error", query)
	}
	pe := &ParseError{}
	if !errors.As(err, &pe) {
		t.Fatalf("CompileQuery(%q): error %v is not a ParseError", query, err)
	}
	return pe
}

func TestCompileRejectsUnknownKindValue(t *testing.T) {
	pe := compileErr(t, "kind:bogus needle")
	if pe.Kind != ParseErrInvalidValue || pe.Field != "kind" {
		t.Fatalf("error = %+v", pe)
	}
	if !strings.Contains(pe.Message, "bogus") || !strings.Contains(pe.Message, HitKindCode) {
		t.Fatalf("message should name the value and the vocabulary: %q", pe.Message)
	}
}

func TestCompileRejectsUnknownSourceValue(t *testing.T) {
	pe := compileErr(t, "source:nope needle")
	if pe.Kind != ParseErrInvalidValue || pe.Field != "source" {
		t.Fatalf("error = %+v", pe)
	}
}

func TestCompileAcceptsEveryDeclaredKind(t *testing.T) {
	for _, kind := range dslFieldValues["kind"] {
		if _, err := CompileQuery("kind:"+kind+" needle", testCompileContext()); err != nil {
			t.Fatalf("kind:%s: %v", kind, err)
		}
	}
}

// TestGrammarVocabulariesMatchHostConstants ties the catalog's closed value
// lists to the constants the host writes, so neither can drift alone.
func TestGrammarVocabulariesMatchHostConstants(t *testing.T) {
	wantKinds := []string{
		HitKindEvidence, HitKindMessage, HitKindClaim, HitKindFinding, HitKindWeb,
		HitKindCode, HitKindFile, HitKindSymbol, HitKindTool, HitKindNetwork, HitKindArtifact,
		HitKindOutcome, scanKind,
	}
	wantSources := []string{
		SourceMessage, SourceTool, SourceFinding, SourceArtifact, SourceGateEvidence, SourceCode,
	}
	assertSameSet := func(field string, want []string) {
		t.Helper()
		got := map[string]bool{}
		for _, v := range dslFieldValues[field] {
			got[v] = true
		}
		if len(got) != len(want) {
			t.Fatalf("%s vocabulary = %v, want %v", field, dslFieldValues[field], want)
		}
		for _, v := range want {
			if !got[v] {
				t.Fatalf("%s vocabulary missing %q (grammar.json out of step with host constants)", field, v)
			}
		}
	}
	assertSameSet("kind", wantKinds)
	assertSameSet("source", wantSources)

}

func TestCompileRejectsContradictoryKindPair(t *testing.T) {
	pe := compileErr(t, "kind:code needle kind:web")
	if pe.Kind != ParseErrInvalidValue || pe.Field != "kind" {
		t.Fatalf("error = %+v", pe)
	}
	if !strings.Contains(pe.Message, "OR") {
		t.Fatalf("message should point at OR: %q", pe.Message)
	}
}

func TestCompileRejectsRequiredAndNegatedSameValue(t *testing.T) {
	pe := compileErr(t, "kind:code needle NOT kind:code")
	if pe.Kind != ParseErrInvalidValue || pe.Field != "kind" {
		t.Fatalf("error = %+v", pe)
	}
}

func TestCompileAllowsSameFieldAlternativesUnderOr(t *testing.T) {
	for _, query := range []string{
		"(kind:code OR kind:web) needle",
		"needle (kind:code OR kind:message OR kind:web)",
		// De Morgan: a negated AND is alternatives, not a contradiction.
		"needle NOT (kind:code kind:web)",
		// Repeated equal values are redundant, not contradictory.
		"kind:code needle kind:code",
		// Negations of different values are compatible.
		"NOT kind:message NOT kind:file NOT kind:code needle",
	} {
		if _, err := CompileQuery(query, testCompileContext()); err != nil {
			t.Fatalf("CompileQuery(%q): %v", query, err)
		}
	}
}

func TestCompileRejectsNegatedOrContradiction(t *testing.T) {
	// NOT (kind:code OR x) requires NOT kind:code — contradicts kind:code.
	pe := compileErr(t, "kind:code needle NOT (kind:code OR kind:web)")
	if pe.Kind != ParseErrInvalidValue || pe.Field != "kind" {
		t.Fatalf("error = %+v", pe)
	}
}

func TestInterpretationCarriesFilterPolarity(t *testing.T) {
	plan, err := CompileQuery("kind:code needle NOT kind:message project:current", testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	filters := plan.Interpretation.Filters
	want := []InterpretedFilter{
		{Field: "kind", Value: "code", Negated: false},
		{Field: "kind", Value: "message", Negated: true},
		{Field: "project", Value: "current", Negated: false},
	}
	if len(filters) != len(want) {
		t.Fatalf("filters = %+v", filters)
	}
	for i, f := range want {
		if filters[i] != f {
			t.Fatalf("filters[%d] = %+v, want %+v", i, filters[i], f)
		}
	}
}

func TestQuotedPhraseStaysWholeAcrossLegs(t *testing.T) {
	plan, err := CompileQuery(`"quick brown" kind:message`, testCompileContext())
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if len(plan.Interpretation.FTSTerms) != 1 || plan.Interpretation.FTSTerms[0] != "quick brown" {
		t.Fatalf("fts terms = %v", plan.Interpretation.FTSTerms)
	}
	// The store leg quotes the phrase as one FTS token sequence.
	if got := buildFTSMatch("quick brown", true); got != `"quick brown"` {
		t.Fatalf("phrase match = %q", got)
	}
	// Unquoted multi-word text still splits into AND terms.
	if got := buildFTSMatch("quick brown", false); got != `"quick" AND "brown"` {
		t.Fatalf("split match = %q", got)
	}
}

func TestParseDurationRejectsOverflowingCount(t *testing.T) {
	if _, err := parseDuration("99999999999999999999d"); err == nil {
		t.Fatal("expected overflow rejection")
	}
	if d, err := parseDuration("30d"); err != nil || d.Hours() != 30*24 {
		t.Fatalf("30d = %v, %v", d, err)
	}
}

func TestLineExcludesDropDependencyTreesByDefault(t *testing.T) {
	ctx := testCompileContext()
	ctx.DependencyPathPatterns = []string{"node_modules", "dist"}
	plan, err := CompileQuery("needle project:current", ctx)
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Code == nil {
		t.Fatal("expected code leg")
	}
	if len(plan.Code.LineExcludeDirs) != 2 {
		t.Fatalf("line excludes = %v", plan.Code.LineExcludeDirs)
	}
}

func TestExplicitPathReadmitsTargetedExclude(t *testing.T) {
	ctx := testCompileContext()
	ctx.DependencyPathPatterns = []string{"node_modules", "dist"}
	plan, err := CompileQuery("needle path:node_modules/react/* project:current", ctx)
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Code == nil {
		t.Fatal("expected code leg")
	}
	// The named tree is searchable again; the unnamed one stays excluded.
	if len(plan.Code.LineExcludeDirs) != 1 || plan.Code.LineExcludeDirs[0] != "dist" {
		t.Fatalf("line excludes = %v", plan.Code.LineExcludeDirs)
	}
	// File-name hits keep the full exclusion floor.
	if len(plan.Code.FileExcludeDirs) != 2 {
		t.Fatalf("file excludes = %v", plan.Code.FileExcludeDirs)
	}
}

func TestIncludeGlobReadmitsTargetedExclude(t *testing.T) {
	ctx := testCompileContext()
	ctx.DependencyPathPatterns = []string{"node_modules"}
	ctx.Flags = MatchFlags{Include: []string{"node_modules/**"}}
	plan, err := CompileQuery("needle project:current", ctx)
	if err != nil {
		t.Fatalf("CompileQuery: %v", err)
	}
	if plan.Code == nil {
		t.Fatal("expected code leg")
	}
	if len(plan.Code.LineExcludeDirs) != 0 {
		t.Fatalf("line excludes = %v", plan.Code.LineExcludeDirs)
	}
}
