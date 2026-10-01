package grammargen

import (
	"context"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

// tree-sitter's C CLI compiles {n,} with n >= 2 as exactly {n}: in
// expand_tokens.rs the (min, None) arm builds the zero_or_more loop exiting
// to next_state_id, then builds the min-count chain ALSO exiting straight to
// next_state_id, so the loop is never wired in and stays unreachable. The C
// oracle therefore lexes `\x0000000` inside a Go interpreted string literal
// as escape_sequence `\x00` (exactly 2 hex digits) + content `00000`, while
// a faithful regex reading would consume all 7 hex digits. C oracle behavior
// wins: {n,} (n >= 2) must truncate to {n}. {0,} and {1,} hit the dedicated
// star/plus arms in the C CLI and stay unbounded.

// TestUnboundedCountedRepetitionTruncatesLikeC reproduces the go corpus
// divergence (archive/tar/strconv_test.go bytes [2066:2082], string
// `" \x0000000\x00"`) at the lexer level, via the same token-flattening
// path the real go grammar takes (Pat leaves inside an ImmToken).
func TestUnboundedCountedRepetitionTruncatesLikeC(t *testing.T) {
	escRule, err := flattenTokenInner(Seq(
		Str(`\`),
		Choice(
			Pat(`[^xuU]`),
			Pat(`\d{2,3}`),
			Pat(`x[0-9a-fA-F]{2,}`),
			Pat(`u[0-9a-fA-F]{4}`),
			Pat(`U[0-9a-fA-F]{8}`),
		),
	))
	if err != nil {
		t.Fatalf("flatten escape rule: %v", err)
	}
	content, err := expandPatternRule(`[^"\n\\]+`)
	if err != nil {
		t.Fatalf("expand content pattern: %v", err)
	}
	lexStates, modeOffsets, err := buildLexDFA(
		context.Background(),
		[]TerminalPattern{
			{SymbolID: 1, Rule: escRule, Priority: 0},
			{SymbolID: 2, Rule: content, Priority: 0},
		},
		nil,
		nil,
		[]lexModeSpec{{
			validSymbols: map[int]bool{1: true, 2: true},
		}},
	)
	if err != nil {
		t.Fatalf("buildLexDFA: %v", err)
	}

	lexer := gotreesitter.NewLexer(lexStates, []byte(`\x0000000`))
	tok := lexer.Next(uint32(modeOffsets[0]))
	if got, want := tok.Symbol, gotreesitter.Symbol(1); got != want {
		t.Fatalf("token symbol = %d, want %d (escape_sequence)", got, want)
	}
	if got, want := tok.EndByte, uint32(4); got != want {
		t.Fatalf(`escape_sequence end = %d, want %d (\x00 — exactly 2 hex digits like the C tables)`, got, want)
	}
}

// TestUnboundedCountedRepetitionTruncatesViaDirectPattern covers the second
// compilation path: a RulePattern leaf reaching the NFA builder directly
// (buildPattern -> buildFromRegexNode), without token flattening.
func TestUnboundedCountedRepetitionTruncatesViaDirectPattern(t *testing.T) {
	lexStates, modeOffsets, err := buildLexDFA(
		context.Background(),
		[]TerminalPattern{
			{SymbolID: 1, Rule: Pat(`x[0-9a-fA-F]{2,}`), Priority: 0},
		},
		nil,
		nil,
		[]lexModeSpec{{
			validSymbols: map[int]bool{1: true},
		}},
	)
	if err != nil {
		t.Fatalf("buildLexDFA: %v", err)
	}

	lexer := gotreesitter.NewLexer(lexStates, []byte(`x00abcd`))
	tok := lexer.Next(uint32(modeOffsets[0]))
	if got, want := tok.Symbol, gotreesitter.Symbol(1); got != want {
		t.Fatalf("token symbol = %d, want %d", got, want)
	}
	if got, want := tok.EndByte, uint32(3); got != want {
		t.Fatalf("token end = %d, want %d (x + exactly 2 hex digits)", got, want)
	}
}

// TestLowCountAndBoundedRepetitionsKeepRegexSemantics guards the cases the
// C CLI compiles correctly: {0,} stays star, {1,} stays plus, and bounded
// {n,m} keeps its optional tail.
func TestLowCountAndBoundedRepetitionsKeepRegexSemantics(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		input   string
		wantEnd uint32
	}{
		{"zero_or_more", `ba{0,}`, "baaa", 4},
		{"one_or_more", `a{1,}`, "aaaa", 4},
		{"bounded_two_three", `[0-9]{2,3}`, "12345", 3},
		{"exact_two", `[0-9]{2}`, "12345", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule, err := expandPatternRule(tc.pattern)
			if err != nil {
				t.Fatalf("expand %q: %v", tc.pattern, err)
			}
			lexStates, modeOffsets, err := buildLexDFA(
				context.Background(),
				[]TerminalPattern{
					{SymbolID: 1, Rule: rule, Priority: 0},
				},
				nil,
				nil,
				[]lexModeSpec{{
					validSymbols: map[int]bool{1: true},
				}},
			)
			if err != nil {
				t.Fatalf("buildLexDFA: %v", err)
			}
			lexer := gotreesitter.NewLexer(lexStates, []byte(tc.input))
			tok := lexer.Next(uint32(modeOffsets[0]))
			if got, want := tok.Symbol, gotreesitter.Symbol(1); got != want {
				t.Fatalf("token symbol = %d, want %d", got, want)
			}
			if got := tok.EndByte; got != tc.wantEnd {
				t.Fatalf("token end = %d, want %d", got, tc.wantEnd)
			}
		})
	}
}

// TestGoHexEscapeStringLiteralTreeShape locks the end-to-end tree shape for
// the corpus repro: `" \x0000000\x00"` must parse as content, \x00 escape,
// 00000 content, \x00 escape — matching the C oracle.
func TestGoHexEscapeStringLiteralTreeShape(t *testing.T) {
	g := NewGrammar("hex_escape_truncation")
	g.Define("source_file", Sym("interpreted_string_literal"))
	g.Define("interpreted_string_literal", Seq(
		Str(`"`),
		Repeat(Choice(
			Alias(
				ImmToken(Prec(1, Pat(`[^"\n\\]+`))),
				"interpreted_string_literal_content", true,
			),
			Sym("escape_sequence"),
		)),
		ImmToken(Str(`"`)),
	))
	g.Define("escape_sequence", ImmToken(Seq(
		Str(`\`),
		Choice(
			Pat(`[^xuU]`),
			Pat(`\d{2,3}`),
			Pat(`x[0-9a-fA-F]{2,}`),
			Pat(`u[0-9a-fA-F]{4}`),
			Pat(`U[0-9a-fA-F]{8}`),
		),
	)))

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}
	tree, err := gotreesitter.NewParser(lang).Parse([]byte(`" \x0000000\x00"`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	want := "(source_file (interpreted_string_literal (interpreted_string_literal_content) (escape_sequence) (interpreted_string_literal_content) (escape_sequence)))"
	if got := tree.RootNode().SExpr(lang); got != want {
		t.Fatalf("SExpr = %s, want %s", got, want)
	}
}
