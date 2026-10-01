package grammargen

import (
	"reflect"
	"testing"
)

func TestMissingRecoveryTokensFollowOriginGotoClosure(t *testing.T) {
	const (
		tokenCount = 4
		missingID  = 1
		semicolon  = 2
		unrelated  = 3
		name       = 4
		directive  = 5
		other      = 6
	)

	tables := &LRTables{
		StateCount: 9,
		ActionTable: map[int]map[int][]lrAction{
			0: {
				missingID: {{kind: lrShift, state: 1}},
			},
			1: {
				semicolon: {{kind: lrReduce, lhsSym: name}},
				unrelated: {{kind: lrReduce, lhsSym: other}},
			},
			2: {
				semicolon: {{kind: lrReduce, lhsSym: directive}},
			},
			3: {
				semicolon: {{kind: lrShift, state: 4}},
			},
			8: {
				unrelated: {{kind: lrShift, state: 9}},
			},
		},
		GotoTable: map[int]map[int]int{
			0: {
				name:      2,
				directive: 3,
			},
			7: {
				other: 8,
			},
		},
		ExtraChainStateStart: -1,
	}

	fn := buildMissingRecoveryTokensFunc(tables, tokenCount, nil, nil)
	if fn == nil {
		t.Fatal("missing recovery token function is nil")
	}
	if got, want := fn(0), []int{semicolon}; !reflect.DeepEqual(got, want) {
		t.Fatalf("missing recovery tokens for state 0 = %v, want %v", got, want)
	}
}

func TestMissingRecoveryTokensIgnoreExtraAndUnrelatedShifts(t *testing.T) {
	const (
		tokenCount = 4
		missingID  = 1
		semicolon  = 2
		unrelated  = 3
		name       = 4
	)

	tables := &LRTables{
		StateCount: 5,
		ActionTable: map[int]map[int][]lrAction{
			0: {
				missingID: {{kind: lrShift, state: 1, isExtra: true}},
			},
			1: {
				semicolon: {{kind: lrReduce, lhsSym: name}},
			},
			2: {
				semicolon: {{kind: lrShift, state: 3}},
			},
			4: {
				unrelated: {{kind: lrShift, state: 5}},
			},
		},
		GotoTable: map[int]map[int]int{
			0: {name: 2},
		},
		ExtraChainStateStart: -1,
	}

	fn := buildMissingRecoveryTokensFunc(tables, tokenCount, nil, nil)
	if got := fn(0); len(got) != 0 {
		t.Fatalf("extra missing-token shift produced recovery tokens %v, want none", got)
	}

	tables.ActionTable[0][missingID] = []lrAction{{kind: lrShift, state: 1}}
	delete(tables.GotoTable[0], name)
	if got := buildMissingRecoveryTokensFunc(tables, tokenCount, nil, nil)(0); len(got) != 0 {
		t.Fatalf("unreachable origin goto produced recovery tokens %v, want none", got)
	}
}

func TestMissingRecoveryTokensDoNotAddSequenceInternalTokenOverlappingDirectShift(t *testing.T) {
	const (
		tokenCount = 4
		hashBang   = 1
		body       = 2
		newline    = 3
	)

	tables := &LRTables{
		StateCount: 3,
		ActionTable: map[int]map[int][]lrAction{
			0: {
				hashBang: {{kind: lrShift, state: 1}},
			},
			1: {
				body: {{kind: lrShift, state: 2}},
			},
			2: {
				newline: {{kind: lrShift, state: 3}},
			},
		},
		ExtraChainStateStart: -1,
	}
	patterns := []TerminalPattern{
		{SymbolID: hashBang, Rule: Str("#!")},
		{SymbolID: body, Rule: Pat(".+")},
		{SymbolID: newline, Rule: Str("\n")},
	}

	if got := buildMissingRecoveryTokensFunc(tables, tokenCount, patterns, nil)(0); len(got) != 0 {
		t.Fatalf("script-tag-like sequence widened state 0 with %v, want none", got)
	}
}

func TestMissingRecoveryTokensDoNotAddLongOperatorOverlappingDirectShift(t *testing.T) {
	const (
		tokenCount = 7
		missing    = 1
		bar        = 2
		barBar     = 3
		dotDot     = 4
		dotDotEq   = 5
	)

	tables := &LRTables{
		StateCount: 5,
		ActionTable: map[int]map[int][]lrAction{
			0: {
				missing: {{kind: lrShift, state: 1}},
				bar:     {{kind: lrShift, state: 2}},
				dotDot:  {{kind: lrShift, state: 3}},
			},
			1: {
				barBar:   {{kind: lrShift, state: 4}},
				dotDotEq: {{kind: lrShift, state: 5}},
			},
		},
		ExtraChainStateStart: -1,
	}
	patterns := []TerminalPattern{
		{SymbolID: bar, Rule: Str("|")},
		{SymbolID: barBar, Rule: Str("||")},
		{SymbolID: dotDot, Rule: Str("..")},
		{SymbolID: dotDotEq, Rule: Str("..=")},
	}

	if got := buildMissingRecoveryTokensFunc(tables, tokenCount, patterns, nil)(0); len(got) != 0 {
		t.Fatalf("overlapping operator recovery tokens = %v, want none", got)
	}
}

func TestMissingRecoveryTokensDoNotAddLookaheadOverSkippedExtra(t *testing.T) {
	const (
		tokenCount = 4
		missing    = 1
		whitespace = 2
		newline    = 3
		name       = 4
		directive  = 5
	)

	tables := &LRTables{
		StateCount: 4,
		ActionTable: map[int]map[int][]lrAction{
			0: {
				missing: {{kind: lrShift, state: 1}},
			},
			1: {
				newline: {{kind: lrReduce, lhsSym: name}},
			},
			2: {
				newline: {{kind: lrReduce, lhsSym: directive}},
			},
			3: {
				newline: {{kind: lrShift, state: 4}},
			},
		},
		GotoTable: map[int]map[int]int{
			0: {
				name:      2,
				directive: 3,
			},
		},
		ExtraChainStateStart: -1,
	}
	patterns := []TerminalPattern{
		{SymbolID: whitespace, Rule: Pat(`\s`), Priority: 2000},
		{SymbolID: newline, Rule: Pat(`\r?\n`), Priority: -500, Immediate: true},
	}
	skipExtras := map[int]bool{whitespace: true}

	if got := buildMissingRecoveryTokensFunc(tables, tokenCount, patterns, skipExtras)(0); len(got) != 0 {
		t.Fatalf("skipped-extra-overlapping recovery tokens = %v, want none", got)
	}
}

func TestMissingRecoveryTokensStillAddNonOverlappingLookahead(t *testing.T) {
	const (
		tokenCount = 3
		nameToken  = 1
		semicolon  = 2
		name       = 3
		directive  = 4
	)

	tables := &LRTables{
		StateCount: 4,
		ActionTable: map[int]map[int][]lrAction{
			0: {
				nameToken: {{kind: lrShift, state: 1}},
			},
			1: {
				semicolon: {{kind: lrReduce, lhsSym: name}},
			},
			2: {
				semicolon: {{kind: lrReduce, lhsSym: directive}},
			},
			3: {
				semicolon: {{kind: lrShift, state: 4}},
			},
		},
		GotoTable: map[int]map[int]int{
			0: {
				name:      2,
				directive: 3,
			},
		},
		ExtraChainStateStart: -1,
	}
	patterns := []TerminalPattern{
		{SymbolID: nameToken, Rule: Str("library")},
		{SymbolID: semicolon, Rule: Str(";")},
	}

	if got, want := buildMissingRecoveryTokensFunc(tables, tokenCount, patterns, nil)(0), []int{semicolon}; !reflect.DeepEqual(got, want) {
		t.Fatalf("missing recovery tokens for non-overlapping lookahead = %v, want %v", got, want)
	}
}

func TestMissingRecoveryLexModeWideningStillAppliesToMainStates(t *testing.T) {
	const (
		tokenCount = 3
		nameToken  = 1
		semicolon  = 2
	)

	lexModes, stateToMode, _ := computeLexModes(
		1,
		tokenCount,
		func(state, sym int) bool {
			return state == 0 && sym == nameToken
		},
		nil,
		nil,
		-1,
		nil,
		nil,
		0,
		nil,
		map[int]bool{nameToken: true, semicolon: true},
		nil,
		func(state int) []int {
			if state == 0 {
				return []int{semicolon}
			}
			return nil
		},
		nil,
		nil,
		nil,
	)

	mode := lexModes[stateToMode[0]]
	if !mode.validSymbols[nameToken] {
		t.Fatal("main state lost its direct token")
	}
	if !mode.validSymbols[semicolon] {
		t.Fatal("main state missing-recovery lookahead was not added to lex mode")
	}
	if mode.preferredSymbols[semicolon] {
		t.Fatal("missing-recovery lookahead should not become a preferred direct token")
	}
}

// TestMissingRecoveryLexModeWideningSkippedForStrictImmediateStates guards the
// strictImmediateState carve-out added to computeLexModesWithContext: a state
// whose only real (non-widened) action is a token.immediate() terminal must
// not have missing-token-recovery lookahead widening (or terminal extras)
// injected into its lex mode. Passing actual immediate tokens here (unlike
// the sibling TestMissingRecoveryLexModeWideningStillAppliesToMainStates
// above, which passes immediateTokens=nil and so never exercises this path)
// is what actually exercises strictImmediateState — see the false-ERROR
// class this fixed: grammargen/dfa.go unconditionally widened every lex mode
// with reduce-follow/missing-recovery lookaheads and terminal extras, which
// corrupted the immediate-vs-non-immediate tie-break used when generating
// the DFA for token.immediate() runs (e.g. mid interpreted_string_literal).
func TestMissingRecoveryLexModeWideningSkippedForStrictImmediateStates(t *testing.T) {
	const (
		tokenCount   = 3
		immediateTok = 1
		semicolon    = 2
	)

	lexModes, stateToMode, _ := computeLexModes(
		1,
		tokenCount,
		func(state, sym int) bool {
			// State 0's only real action is on the immediate token.
			return state == 0 && sym == immediateTok
		},
		nil,
		nil,
		-1,
		map[int]bool{immediateTok: true},
		nil,
		0,
		nil,
		map[int]bool{immediateTok: true, semicolon: true},
		nil,
		func(state int) []int {
			if state == 0 {
				return []int{semicolon}
			}
			return nil
		},
		nil,
		nil,
		nil,
	)

	mode := lexModes[stateToMode[0]]
	if !mode.validSymbols[immediateTok] {
		t.Fatal("strict-immediate state lost its direct token")
	}
	if mode.validSymbols[semicolon] {
		t.Fatal("missing-recovery widening leaked into a strict-immediate state's lex mode")
	}
}

// TestMissingRecoveryLexModeWideningStillAppliesToMixedImmediateStates is the
// counterpart to the strict-immediate carve-out above: a state with BOTH an
// immediate action and a non-immediate (ordinary) action is not "strict
// immediate" and must keep the existing missing-token-recovery widening
// behavior.
func TestMissingRecoveryLexModeWideningStillAppliesToMixedImmediateStates(t *testing.T) {
	const (
		tokenCount   = 4
		immediateTok = 1
		ordinaryTok  = 2
		semicolon    = 3
	)

	lexModes, stateToMode, _ := computeLexModes(
		1,
		tokenCount,
		func(state, sym int) bool {
			// State 0 has both an immediate action and an ordinary
			// (non-immediate) action — a mixed state.
			return state == 0 && (sym == immediateTok || sym == ordinaryTok)
		},
		nil,
		nil,
		-1,
		map[int]bool{immediateTok: true},
		nil,
		0,
		nil,
		map[int]bool{immediateTok: true, ordinaryTok: true, semicolon: true},
		nil,
		func(state int) []int {
			if state == 0 {
				return []int{semicolon}
			}
			return nil
		},
		nil,
		nil,
		nil,
	)

	mode := lexModes[stateToMode[0]]
	if !mode.validSymbols[immediateTok] || !mode.validSymbols[ordinaryTok] {
		t.Fatal("mixed state lost one of its direct tokens")
	}
	if !mode.validSymbols[semicolon] {
		t.Fatal("mixed state must still receive missing-recovery lookahead widening")
	}
}

func TestMissingRecoveryTokensStillAddSameFirstRuneNonPreemptingLookahead(t *testing.T) {
	const (
		tokenCount = 3
		directAB   = 1
		recoverAC  = 2
		name       = 3
		directive  = 4
	)

	tables := &LRTables{
		StateCount: 4,
		ActionTable: map[int]map[int][]lrAction{
			0: {
				directAB: {{kind: lrShift, state: 1}},
			},
			1: {
				recoverAC: {{kind: lrReduce, lhsSym: name}},
			},
			2: {
				recoverAC: {{kind: lrReduce, lhsSym: directive}},
			},
			3: {
				recoverAC: {{kind: lrShift, state: 4}},
			},
		},
		GotoTable: map[int]map[int]int{
			0: {
				name:      2,
				directive: 3,
			},
		},
		ExtraChainStateStart: -1,
	}
	patterns := []TerminalPattern{
		{SymbolID: directAB, Rule: Str("ab")},
		{SymbolID: recoverAC, Rule: Str("ac")},
	}

	if got, want := buildMissingRecoveryTokensFunc(tables, tokenCount, patterns, nil)(0), []int{recoverAC}; !reflect.DeepEqual(got, want) {
		t.Fatalf("missing recovery tokens for same-first-rune non-preempting lookahead = %v, want %v", got, want)
	}
}

// TestMissingRecoveryTokensSkippedExtraPreemptionCacheConsistentAcrossStates
// guards the skip-extra preemption memoization added to
// buildMissingRecoveryTokensFuncWithContext (a map in the same closure scope
// as preemptionCache, keyed by lookahead alone since preemptsSkippedTerminalExtra
// depends only on lookahead for a fixed patternsBySymbol/skipExtras). The
// cache is shared across every state queried from the returned closure, so
// this test builds TWO independent "missing token" origins (state 0 and
// state 20) that both resolve the SAME two lookaheads — one skip-extra-
// preempted (newline, shadowed by the whitespace extra exactly like
// TestMissingRecoveryTokensDoNotAddLookaheadOverSkippedExtra above) and one
// not preempted (comma) — and checks both origins agree with the correct,
// uncached answer regardless of which state's query populates the cache
// first. A cache keyed or scoped incorrectly (e.g. accidentally reused
// across distinct lookaheads, or not actually shared and silently
// re-deriving a stale/wrong answer) would make one of the two origins
// diverge from the other.
func TestMissingRecoveryTokensSkippedExtraPreemptionCacheConsistentAcrossStates(t *testing.T) {
	const (
		tokenCount = 6
		missingA   = 1
		missingB   = 2
		whitespace = 3
		newline    = 4
		comma      = 5
	)

	tables := &LRTables{
		StateCount: 24,
		ActionTable: map[int]map[int][]lrAction{
			// Origin A: state 0 --missingA--> state 1, which directly shifts
			// both lookaheads.
			0: {
				missingA: {{kind: lrShift, state: 1}},
			},
			1: {
				newline: {{kind: lrShift, state: 2}},
				comma:   {{kind: lrShift, state: 3}},
			},
			// Origin B: an independent copy rooted at state 20.
			20: {
				missingB: {{kind: lrShift, state: 21}},
			},
			21: {
				newline: {{kind: lrShift, state: 22}},
				comma:   {{kind: lrShift, state: 23}},
			},
		},
		ExtraChainStateStart: -1,
	}
	patterns := []TerminalPattern{
		{SymbolID: whitespace, Rule: Pat(`\s`), Priority: 2000},
		{SymbolID: newline, Rule: Pat(`\r?\n`), Priority: -500, Immediate: true},
		{SymbolID: comma, Rule: Str(",")},
	}
	skipExtras := map[int]bool{whitespace: true}
	want := []int{comma}

	// Populate the shared skip-extra preemption cache from origin A first,
	// then reuse it from origin B.
	fnAB := buildMissingRecoveryTokensFunc(tables, tokenCount, patterns, skipExtras)
	if got := fnAB(0); !reflect.DeepEqual(got, want) {
		t.Fatalf("origin A (populates cache) missing recovery tokens = %v, want %v", got, want)
	}
	if got := fnAB(20); !reflect.DeepEqual(got, want) {
		t.Fatalf("origin B (reuses cache) missing recovery tokens = %v, want %v", got, want)
	}

	// Reverse the query order with a fresh function/cache so the memoized
	// skip-extra preemption result cannot depend on which state populates it
	// first.
	fnBA := buildMissingRecoveryTokensFunc(tables, tokenCount, patterns, skipExtras)
	if got := fnBA(20); !reflect.DeepEqual(got, want) {
		t.Fatalf("origin B (populates cache) missing recovery tokens = %v, want %v", got, want)
	}
	if got := fnBA(0); !reflect.DeepEqual(got, want) {
		t.Fatalf("origin A (reuses cache) missing recovery tokens = %v, want %v", got, want)
	}
}
