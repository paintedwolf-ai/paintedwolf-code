package grammargen

import (
	"context"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestBuildLexDFAPrefersLongerStringOverSingleCharPattern(t *testing.T) {
	lexStates, modeOffsets, err := buildLexDFA(
		context.Background(),
		[]TerminalPattern{
			{SymbolID: 1, Rule: Pat(`[^\n]`), Priority: 0},
			{SymbolID: 2, Rule: Str("*/"), Priority: 0},
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
	if len(modeOffsets) != 1 {
		t.Fatalf("len(modeOffsets) = %d, want 1", len(modeOffsets))
	}

	lexer := gotreesitter.NewLexer(lexStates, []byte("*/"))
	tok := lexer.Next(uint32(modeOffsets[0]))
	if got, want := tok.Symbol, gotreesitter.Symbol(2); got != want {
		t.Fatalf("token symbol = %d, want %d", got, want)
	}
	if got, want := tok.EndByte, uint32(2); got != want {
		t.Fatalf("token end = %d, want %d", got, want)
	}
}

func TestBuildLexDFAPrefersExtractionOrderForSameLengthTie(t *testing.T) {
	integer, err := expandPatternRule(`\d+`)
	if err != nil {
		t.Fatalf("expand integer: %v", err)
	}
	unquoted, err := expandPatternRule(`[^\r\n \t]+`)
	if err != nil {
		t.Fatalf("expand unquoted: %v", err)
	}
	lexStates, modeOffsets, err := buildLexDFA(
		context.Background(),
		[]TerminalPattern{
			{SymbolID: 2, Rule: integer, Priority: 0},
			{SymbolID: 1, Rule: unquoted, Priority: 0},
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
	if len(modeOffsets) != 1 {
		t.Fatalf("len(modeOffsets) = %d, want 1", len(modeOffsets))
	}

	lexer := gotreesitter.NewLexer(lexStates, []byte("0"))
	tok := lexer.Next(uint32(modeOffsets[0]))
	if got, want := tok.Symbol, gotreesitter.Symbol(2); got != want {
		t.Fatalf("same-length token symbol = %d, want %d", got, want)
	}
	if got, want := tok.EndByte, uint32(1); got != want {
		t.Fatalf("same-length token end = %d, want %d", got, want)
	}

	lexer = gotreesitter.NewLexer(lexStates, []byte("3rdparty"))
	tok = lexer.Next(uint32(modeOffsets[0]))
	if got, want := tok.Symbol, gotreesitter.Symbol(1); got != want {
		t.Fatalf("longer token symbol = %d, want %d", got, want)
	}
	if got, want := tok.EndByte, uint32(8); got != want {
		t.Fatalf("longer token end = %d, want %d", got, want)
	}
}

func TestBuildLexDFAPrefersModeDirectSymbolForSameLengthTie(t *testing.T) {
	word, err := expandPatternRule(`[a-z]+`)
	if err != nil {
		t.Fatalf("expand word: %v", err)
	}
	specific, err := expandPatternRule(`[a-z]+`)
	if err != nil {
		t.Fatalf("expand specific: %v", err)
	}
	lexStates, modeOffsets, err := buildLexDFA(
		context.Background(),
		[]TerminalPattern{
			{SymbolID: 1, Rule: word, Priority: 0},
			{SymbolID: 2, Rule: specific, Priority: 0},
		},
		nil,
		nil,
		[]lexModeSpec{{
			validSymbols:     map[int]bool{1: true, 2: true},
			preferredSymbols: map[int]bool{2: true},
		}},
	)
	if err != nil {
		t.Fatalf("buildLexDFA: %v", err)
	}
	if len(modeOffsets) != 1 {
		t.Fatalf("len(modeOffsets) = %d, want 1", len(modeOffsets))
	}

	lexer := gotreesitter.NewLexer(lexStates, []byte("attributename"))
	tok := lexer.Next(uint32(modeOffsets[0]))
	if got, want := tok.Symbol, gotreesitter.Symbol(2); got != want {
		t.Fatalf("same-length token symbol = %d, want %d", got, want)
	}
	if got, want := tok.EndByte, uint32(len("attributename")); got != want {
		t.Fatalf("same-length token end = %d, want %d", got, want)
	}
}

func TestBuildLexDFADistinguishesModePreferredSymbols(t *testing.T) {
	word, err := expandPatternRule(`[a-z]+`)
	if err != nil {
		t.Fatalf("expand word: %v", err)
	}
	specific, err := expandPatternRule(`[a-z]+`)
	if err != nil {
		t.Fatalf("expand specific: %v", err)
	}
	lexModes, stateToMode, _ := computeLexModes(
		2,
		3,
		func(state, sym int) bool {
			switch state {
			case 0:
				return sym == 1
			case 1:
				return sym == 2
			default:
				return false
			}
		},
		nil,
		nil,
		-1,
		nil,
		nil,
		1,
		nil,
		map[int]bool{1: true, 2: true},
		nil,
		func(state int) []int {
			switch state {
			case 0:
				return []int{2}
			case 1:
				return []int{1}
			default:
				return nil
			}
		},
		nil,
		map[int]bool{1: true, 2: true}, // both symbols are pattern-based ([a-z]+); a genuine same-length tie
		nil,
	)
	if got, want := len(lexModes), 2; got != want {
		t.Fatalf("lex mode count = %d, want %d", got, want)
	}
	if stateToMode[0] == stateToMode[1] {
		t.Fatalf("states with different preferred symbols shared lex mode %d", stateToMode[0])
	}

	lexStates, modeOffsets, err := buildLexDFA(
		context.Background(),
		[]TerminalPattern{
			{SymbolID: 1, Rule: word, Priority: 0},
			{SymbolID: 2, Rule: specific, Priority: 0},
		},
		nil,
		nil,
		lexModes,
	)
	if err != nil {
		t.Fatalf("buildLexDFA: %v", err)
	}
	for state, want := range []gotreesitter.Symbol{1, 2} {
		lexer := gotreesitter.NewLexer(lexStates, []byte("name"))
		tok := lexer.Next(uint32(modeOffsets[stateToMode[state]]))
		if tok.Symbol != want {
			t.Fatalf("state %d token symbol = %d, want %d", state, tok.Symbol, want)
		}
	}
}

// TestLexModePreferredSymbolsIgnoredWithoutPatternTerminals is the
// regression test for the swift.bin / go.bin lex-mode explosion: two
// DIFFERENT fixed-string terminals can never tie on a same-length DFA
// accept, so states whose valid symbols are entirely fixed strings must
// share a lex mode regardless of which of those strings is their own
// (pre-widening) direct action — preferredSymbols only matters once a real
// pattern-based terminal is present to conflict with. Without the
// patternTerminals gate, every state's near-unique direct-action set leaks
// into the mode key unconditionally, multiplying lex modes (and therefore
// compiled DFA states) roughly 1:1 with parser state count.
func TestLexModePreferredSymbolsIgnoredWithoutPatternTerminals(t *testing.T) {
	const (
		tokenCount = 3
		symA       = 1
		symB       = 2
	)
	// Two states, each directly shifting a DIFFERENT fixed-string terminal,
	// but each also widened (e.g. via follow-token expansion) to admit the
	// OTHER terminal too — so both states end up with the identical
	// validSyms={symA,symB} but different preferredSyms (state 0 prefers
	// symA, state 1 prefers symB).
	lexModes, stateToMode, _ := computeLexModes(
		2,
		tokenCount,
		func(state, sym int) bool {
			switch state {
			case 0:
				return sym == symA
			case 1:
				return sym == symB
			default:
				return false
			}
		},
		nil,
		nil,
		-1,
		nil,
		nil,
		0,
		nil,
		map[int]bool{symA: true, symB: true},
		func(state int) []int {
			switch state {
			case 0:
				return []int{symB}
			case 1:
				return []int{symA}
			default:
				return nil
			}
		},
		nil,
		nil,
		nil, // no pattern-based terminals: symA/symB are both fixed strings
		nil, // no zero-width terminals either
	)
	if got, want := len(lexModes), 1; got != want {
		t.Fatalf("lex mode count = %d, want %d (fixed-string-only states must share a mode)", got, want)
	}
	if stateToMode[0] != stateToMode[1] {
		t.Fatalf("states with different preferred symbols but no pattern terminals should share lex mode: got %d and %d", stateToMode[0], stateToMode[1])
	}
}

// TestLexModePreferredSymbolsKeptForZeroWidthTerminal is the regression test
// for generator defect #6 (grammargen/dfa.go hasPatternSym gate): a state
// whose valid symbols are ALL fixed strings does NOT automatically qualify
// for the "no tie possible, drop preferredSyms" fast path from
// TestLexModePreferredSymbolsIgnoredWithoutPatternTerminals above when one of
// those fixed strings is zero-width (its rule can match the empty string,
// e.g. the "\x00" EOF-sentinel terminal tree-sitter-go's statement
// terminator alternative uses, or Pat(`\n`) after expandRegexToRule collapses
// it to a fixed-string RuleString terminal). A zero-width terminal's accept
// requires consuming nothing, so it trivially ties with every other terminal
// valid in the state — the tie is genuine and preferredSyms must survive to
// resolve it, or the zero-width terminal wins over a real, longer token
// everywhere the terminator production is reachable (observed as go.bin's
// statement_list/block spans landing one byte short of every closing `}`).
func TestLexModePreferredSymbolsKeptForZeroWidthTerminal(t *testing.T) {
	const (
		tokenCount = 3
		symA       = 1 // e.g. "\n" (a fixed string after literal-regex collapse)
		symB       = 2 // e.g. "\x00" (the zero-width EOF-sentinel string terminal)
	)
	// Same shape as TestBuildLexDFADistinguishesModePreferredSymbols: two
	// states, each directly shifting a DIFFERENT fixed-string terminal, but
	// each also widened (via missing-token-recovery lookahead, which is
	// applied AFTER the preferredSyms snapshot — see computeLexModesWithContext)
	// to admit the OTHER terminal too. Both states end up with identical
	// validSyms={symA,symB} but preferredSyms stays limited to each state's
	// own direct action (state 0: {symA}, state 1: {symB}). The only
	// difference from TestLexModePreferredSymbolsIgnoredWithoutPatternTerminals
	// is symB is now marked zero-width instead of neither symbol being
	// pattern-based.
	lexModes, stateToMode, _ := computeLexModes(
		2,
		tokenCount,
		func(state, sym int) bool {
			switch state {
			case 0:
				return sym == symA
			case 1:
				return sym == symB
			default:
				return false
			}
		},
		nil,
		nil,
		-1,
		nil,
		nil,
		0,
		nil,
		map[int]bool{symA: true, symB: true},
		nil,
		func(state int) []int {
			switch state {
			case 0:
				return []int{symB}
			case 1:
				return []int{symA}
			default:
				return nil
			}
		},
		nil,
		nil,                      // no pattern-based terminals: symA/symB are both fixed strings
		map[int]bool{symB: true}, // symB can match the empty string
	)
	if got, want := len(lexModes), 2; got != want {
		t.Fatalf("lex mode count = %d, want %d (a zero-width symbol makes the tie genuine, so states must NOT share a mode)", got, want)
	}
	if stateToMode[0] == stateToMode[1] {
		t.Fatalf("states with different preferred symbols sharing a zero-width tie-risk symbol must not share lex mode %d", stateToMode[0])
	}
}

func TestKeywordLikeInlinePatternClassification(t *testing.T) {
	if !isKeywordLikeInlinePattern(`[sS][uU][bB][gG][rR][aA][pP][hH]`) {
		t.Fatalf("case-insensitive keyword pattern should be keyword-like")
	}
	if isKeywordLikeInlinePattern(`[^\r\n \t]+`) {
		t.Fatalf("broad catch-all pattern should not be keyword-like")
	}
}

func TestBuildLexDFAPreservesStringOperatorBeforeLineComment(t *testing.T) {
	lineComment, err := expandPatternRule(`\/\/.*`)
	if err != nil {
		t.Fatalf("expand line comment: %v", err)
	}
	lexStates, modeOffsets, err := buildLexDFA(
		context.Background(),
		[]TerminalPattern{
			{SymbolID: 1, Rule: Str("//"), Priority: 0},
			{SymbolID: 2, Rule: lineComment, Priority: 0},
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
	if len(modeOffsets) != 1 {
		t.Fatalf("len(modeOffsets) = %d, want 1", len(modeOffsets))
	}

	lexer := gotreesitter.NewLexer(lexStates, []byte("//rest"))
	tok := lexer.Next(uint32(modeOffsets[0]))
	if got, want := tok.Symbol, gotreesitter.Symbol(1); got != want {
		t.Fatalf("token symbol = %d, want %d", got, want)
	}
	if got, want := tok.EndByte, uint32(2); got != want {
		t.Fatalf("token end = %d, want %d", got, want)
	}
}

func TestBuildLexDFADoesNotAddUnavailableTerminalExtraToMode(t *testing.T) {
	catchAll, err := expandPatternRule(`.+`)
	if err != nil {
		t.Fatalf("expand catch-all: %v", err)
	}
	lexStates, modeOffsets, err := buildLexDFA(
		context.Background(),
		[]TerminalPattern{
			{SymbolID: 1, Rule: Str("library"), Priority: 0},
			{SymbolID: 2, Rule: catchAll, Priority: 0},
		},
		[]int{2},
		nil,
		[]lexModeSpec{{
			validSymbols: map[int]bool{1: true},
		}},
	)
	if err != nil {
		t.Fatalf("buildLexDFA: %v", err)
	}
	if len(modeOffsets) != 1 {
		t.Fatalf("len(modeOffsets) = %d, want 1", len(modeOffsets))
	}

	lexer := gotreesitter.NewLexer(lexStates, []byte("library;"))
	tok := lexer.Next(uint32(modeOffsets[0]))
	if got, want := tok.Symbol, gotreesitter.Symbol(1); got != want {
		t.Fatalf("token symbol = %d, want %d", got, want)
	}
	if got, want := tok.EndByte, uint32(len("library")); got != want {
		t.Fatalf("token end = %d, want %d", got, want)
	}
}

func TestLineBreakOnlyRuleDetectsOptionalCRLF(t *testing.T) {
	newline, err := expandPatternRule(`\r?\n`)
	if err != nil {
		t.Fatalf("expand newline pattern: %v", err)
	}
	if !isLineBreakOnlyRule(newline) {
		t.Fatalf(`\r?\n should be line-break-only`)
	}

	whitespace, err := expandPatternRule(`\s`)
	if err != nil {
		t.Fatalf("expand whitespace pattern: %v", err)
	}
	if isLineBreakOnlyRule(whitespace) {
		t.Fatalf(`\s should not be line-break-only`)
	}
}

func TestCollectTransitionMovesMatchesLegacyRanges(t *testing.T) {
	n := &nfa{
		states: []nfaState{
			{transitions: []nfaTransition{
				{lo: 'a', hi: 'f', nextState: 1},
				{lo: 'd', hi: 'h', nextState: 2},
			}},
			{transitions: []nfaTransition{
				{lo: 'b', hi: 'e', nextState: 3},
			}},
			{},
			{},
		},
		start: 0,
	}

	states := []int{0, 1}
	legacyRanges := collectTransitionRanges(n, states)
	moves := collectTransitionMoves(n, states)
	if len(moves) != len(legacyRanges) {
		t.Fatalf("len(moves) = %d, want %d", len(moves), len(legacyRanges))
	}

	for i, move := range moves {
		if move.lo != legacyRanges[i].lo || move.hi != legacyRanges[i].hi {
			t.Fatalf("move[%d] range = [%q,%q], want [%q,%q]", i, move.lo, move.hi, legacyRanges[i].lo, legacyRanges[i].hi)
		}
		want := moveTargets(n, states, legacyRanges[i].lo, legacyRanges[i].hi)
		if !sameIntSlice(move.targets, want) {
			t.Fatalf("move[%d] targets = %v, want %v", i, move.targets, want)
		}
	}
}
