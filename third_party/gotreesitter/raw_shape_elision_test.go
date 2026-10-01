package gotreesitter

import "testing"

// buildPrefixForkLanguage constructs a hand-built LR grammar with a genuine
// single-stack prefix reduce, followed by a real GLR reduce/reduce conflict
// (fork). It exists to differentially test the single-stack raw-shape
// capture elision (see gssScratch.mayElideRawShape /
// spore.2026-07-12.hazel.rawshape-elision-rca): the "pre" node is reduced
// while exactly one GLR stack is live (a candidate for elision), and it is
// then embedded as a child of both fork alternatives, so the fork's
// tie-break comparison (compareRawStackEntriesRec, via
// compareAcceptedStackRawShapePreference) walks back through it.
//
//	pre  -> x            (production 0, 1 child)   -- single-stack, deterministic
//	A    -> pre x         (production 1, 2 children) DynamicPrecedence 0
//	B    -> pre x         (production 2, 2 children) DynamicPrecedence 0
//
// Symbols: 0 EOF, 1 x (terminal), 2 pre, 3 A, 4 B, 5 y (terminal).
//
// States:
//
//	State 0 (start):        x -> shift(1); pre -> goto(2); A -> goto(4); B -> goto(4)
//	State 1 (saw x):        any -> reduce pre -> x (1 child)
//	State 2 (saw pre):      x -> shift(3)
//	State 3 (saw pre, x):   y -> FORK: reduce A->pre x (prod 1) OR reduce B->pre x (prod 2)
//	State 4 (saw A or B):   y -> shift(5)
//	State 5 (saw A/B, y):   EOF -> accept
//
// Feeding "x x y" shifts once, reduces "pre" deterministically while
// singleStackMode is true (never having forked yet), shifts the second x,
// and only then hits the reduce/reduce conflict that forks the stack on the
// still-pending "y" lookahead. The trailing "y" is deliberate: reduces do not
// consume the lookahead token, so without it the fork and both alternatives'
// accept would resolve within the single iteration that detects the
// conflict, and the peak stack count (sampled only at each outer loop
// iteration's top, see maxStacksSeen in parser.go) would never observe the
// fork. Requiring both alternatives to independently shift "y" forces a real
// iteration boundary (a fresh token acquisition) while 2 stacks are alive.
func buildPrefixForkLanguage() *Language {
	return &Language{
		Name:               "prefix_fork",
		SymbolCount:        6,
		TokenCount:         3,
		ExternalTokenCount: 0,
		StateCount:         6,
		LargeStateCount:    0,
		FieldCount:         0,
		ProductionIDCount:  3,

		SymbolNames: []string{"EOF", "x", "pre", "A", "B", "y"},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "x", Visible: true, Named: true},
			{Name: "pre", Visible: true, Named: true},
			{Name: "A", Visible: true, Named: true},
			{Name: "B", Visible: true, Named: true},
			{Name: "y", Visible: true, Named: true},
		},
		FieldNames: []string{""},

		ParseActions: []ParseActionEntry{
			// 0: error / no action
			{Actions: nil},
			// 1: state0, col x: shift to state 1
			{Actions: []ParseAction{{Type: ParseActionShift, State: 1}}},
			// 2: state1, cols EOF/x/y: reduce pre -> x
			{Actions: []ParseAction{{Type: ParseActionReduce, Symbol: 2, ChildCount: 1, ProductionID: 0}}},
			// 3: state0, col pre: goto state 2
			{Actions: []ParseAction{{Type: ParseActionShift, State: 2}}},
			// 4: state2, col x: shift to state 3 (second x)
			{Actions: []ParseAction{{Type: ParseActionShift, State: 3}}},
			// 5: state0, col A: goto state 4
			{Actions: []ParseAction{{Type: ParseActionShift, State: 4}}},
			// 6: state0, col B: goto state 4
			{Actions: []ParseAction{{Type: ParseActionShift, State: 4}}},
			// 7: state3, col y: TWO actions -- GLR reduce/reduce fork
			{Actions: []ParseAction{
				{Type: ParseActionReduce, Symbol: 3, ChildCount: 2, ProductionID: 1, DynamicPrecedence: 0},
				{Type: ParseActionReduce, Symbol: 4, ChildCount: 2, ProductionID: 2, DynamicPrecedence: 0},
			}},
			// 8: state4, col y: shift to state 5
			{Actions: []ParseAction{{Type: ParseActionShift, State: 5}}},
			// 9: state5, col EOF: accept
			{Actions: []ParseAction{{Type: ParseActionAccept}}},
		},

		// Columns: EOF(0), x(1), pre(2), A(3), B(4), y(5)
		ParseTable: [][]uint16{
			// State 0
			{0, 1, 3, 5, 6, 0},
			// State 1: reduce pre->x regardless of lookahead
			{2, 2, 0, 0, 0, 2},
			// State 2: shift second x
			{0, 4, 0, 0, 0, 0},
			// State 3: fork on y
			{0, 0, 0, 0, 0, 7},
			// State 4: shift y
			{0, 0, 0, 0, 0, 8},
			// State 5: accept on EOF
			{9, 0, 0, 0, 0, 0},
		},

		LexModes: []LexMode{
			{LexState: 0},
			{LexState: 0},
			{LexState: 0},
			{LexState: 0},
			{LexState: 0},
			{LexState: 0},
		},

		LexStates: []LexState{
			// State 0: start
			{
				AcceptToken: 0,
				Skip:        false,
				Default:     -1,
				EOF:         -1,
				Transitions: []LexTransition{
					{Lo: 'x', Hi: 'x', NextState: 1},
					{Lo: 'y', Hi: 'y', NextState: 3},
					{Lo: ' ', Hi: ' ', NextState: 2},
				},
			},
			// State 1: accept x (symbol 1)
			{
				AcceptToken: 1,
				Skip:        false,
				Default:     -1,
				EOF:         -1,
			},
			// State 2: whitespace (skip)
			{
				AcceptToken: 0,
				Skip:        true,
				Default:     -1,
				EOF:         -1,
			},
			// State 3: accept y (symbol 5)
			{
				AcceptToken: 5,
				Skip:        false,
				Default:     -1,
				EOF:         -1,
			},
		},
	}
}

// TestPrefixForkLanguageActuallyForks instruments (via ParseRuntime, which
// this hand-built language reaches through the normal parseInternal loop,
// not any forest fast path) that "x x" genuinely drives more than one live
// GLR stack. This is the precondition the differential tests below depend
// on: without a real fork, they would pass vacuously.
func TestPrefixForkLanguageActuallyForks(t *testing.T) {
	lang := buildPrefixForkLanguage()
	tree := mustParse(t, NewParser(lang), []byte("x x y"))
	defer tree.Release()

	rt := tree.ParseRuntime()
	if rt.StopReason != ParseStopAccepted {
		t.Fatalf("StopReason = %v, want accepted", rt.StopReason)
	}
	if rt.MaxStacksSeen <= 1 {
		t.Fatalf("MaxStacksSeen = %d, want > 1 (test input must actually fork)", rt.MaxStacksSeen)
	}
	if rt.MultiStackIterations == 0 {
		t.Fatal("MultiStackIterations = 0, want > 0 (test input must actually fork)")
	}
}

// The zero-bytes-on-pure-single-stack and nonzero-bytes-on-forking
// counterparts live alongside the reclaim/accounting invariant in
// raw_shape_reclaim_test.go
// (TestParseReclaimsRawShapeStorageAccountsForZeroOnSingleStackParse and
// TestParseReclaimsRawShapeStorageAfterAccounting), which is the revision the
// RCA asked for of the pre-existing reclaim test. This file focuses on the
// gate-on-vs-gate-off differential, which is the load-bearing safety gate.

// TestRawShapeElisionDifferentialPureSingleStack is the gate-on-vs-gate-off
// differential for scenario (a): a pure single-stack parse must produce an
// identical tree whether or not the elision optimization is active.
func TestRawShapeElisionDifferentialPureSingleStack(t *testing.T) {
	lang := buildArithmeticLanguage()
	source := []byte("1+2+3+4")

	SetRawShapeElisionDisabledForDiagnostics(false)
	gateOn := mustParse(t, NewParser(lang), source)
	defer gateOn.Release()

	SetRawShapeElisionDisabledForDiagnostics(true)
	defer SetRawShapeElisionDisabledForDiagnostics(false)
	gateOff := mustParse(t, NewParser(lang), source)
	defer gateOff.Release()

	if got, want := gateOn.RootNode().SExpr(lang), gateOff.RootNode().SExpr(lang); got != want {
		t.Fatalf("gate-on tree = %s, gate-off (elision disabled) tree = %s, want identical", got, want)
	}
}

// TestRawShapeElisionDifferentialPrefixThenFork is the load-bearing
// differential gate the RCA asked for: a parse with a genuine single-stack
// prefix (the "pre" reduce, elided under the gate) followed by a real GLR
// fork (proven by TestPrefixForkLanguageActuallyForks) whose tie-break
// (compareAcceptedStackRawShapePreference / compareRawStackEntriesRec) walks
// back through that prefix node. The selected tree and the winning
// alternative (A vs B) must be identical whether the elision gate is on or
// forced off.
func TestRawShapeElisionDifferentialPrefixThenFork(t *testing.T) {
	lang := buildPrefixForkLanguage()
	source := []byte("x x y")

	SetRawShapeElisionDisabledForDiagnostics(false)
	gateOn := mustParse(t, NewParser(lang), source)
	defer gateOn.Release()
	gateOnRuntime := gateOn.ParseRuntime()

	SetRawShapeElisionDisabledForDiagnostics(true)
	defer SetRawShapeElisionDisabledForDiagnostics(false)
	gateOff := mustParse(t, NewParser(lang), source)
	defer gateOff.Release()
	gateOffRuntime := gateOff.ParseRuntime()

	if gateOnRuntime.MaxStacksSeen <= 1 || gateOffRuntime.MaxStacksSeen <= 1 {
		t.Fatalf("MaxStacksSeen gate-on=%d gate-off=%d, want both > 1 (fork precondition)", gateOnRuntime.MaxStacksSeen, gateOffRuntime.MaxStacksSeen)
	}
	if got, want := gateOn.RootNode().Symbol(), gateOff.RootNode().Symbol(); got != want {
		t.Fatalf("gate-on winning symbol = %d, gate-off winning symbol = %d, want identical tie-break selection", got, want)
	}
	if got, want := gateOn.RootNode().SExpr(lang), gateOff.RootNode().SExpr(lang); got != want {
		t.Fatalf("gate-on tree = %s, gate-off (elision disabled) tree = %s, want identical", got, want)
	}
}

// buildPrefixForkHiddenChildLanguage strengthens the prefix-then-fork
// differential per an adversarial review of the initial cut: that cut's fork
// alternatives (A, B) had distinct root symbols, so
// compareRawStackEntriesRec's very first check (`as != bs`,
// parser_reduce.go's compareRawStackEntriesRec) decided the comparison before
// ever descending into the elided "pre" node — the differential passed
// regardless of whether elision was active.
//
// This grammar closes that gap as far as it can be closed with a real,
// reachable parse:
//
//   - the shared single-stack prefix ("pre") is built through an invisible
//     child ("_hidden -> x x"), so pre's RAW shape (if captured) has
//     childCount 1 (one child: _hidden) while its MATERIALIZED view has
//     childCount 2 (buildReduceChildrenWithPath flattens the invisible
//     _hidden's own two children into pre's materialized child list) —
//     exactly the pre/post-flatten mismatch compareRawStackEntriesRec's
//     one-sided-shape fallback (parser_reduce.go's aHasShape != bHasShape
//     branch) has to resolve without inventing an answer.
//
//   - the two fork alternatives (A, B) are wrapped by two separate unary
//     productions into the SAME shared root symbol "Top", so the two
//     accepted stacks being compared for final result selection literally
//     share a root symbol and childCount, forcing
//     compareAcceptedStackRawShapePreference to descend at least one level
//     instead of returning at depth 0.
//
//     _hidden -> x x        (production 0, 2 children, invisible)
//     pre     -> _hidden     (production 1, 1 child)   -- single-stack, elided
//     A       -> pre x        (production 2, 2 children)
//     B       -> pre x        (production 3, 2 children)
//     Top     -> A y  |  Top -> B y   (production 4, 2 children each, same symbol)
//
// Feeding "x x x y" builds "pre" deterministically while singleStackMode is
// true (two x's fold into _hidden, then pre wraps _hidden), consumes a third
// x, and only then forks on the pending "y" lookahead (reduce/reduce
// conflict on symbol A vs symbol B). Both alternatives shift the same "y"
// and reduce into the shared "Top" symbol before accepting.
//
// Empirically (see TestRawShapeElisionDifferentialPrefixThenForkSharedRootSymbol),
// even with the shared "Top" root, the recursive comparison still resolves
// one level down (A's own symbol vs B's own symbol necessarily differ — that
// difference IS the fork) before it would ever reach "pre" a second time from
// a non-identical object. See that test's doc comment for why: this is not a
// property of this specific grammar, it is structural (raw_shape.go
// captureRawShape's doc comment states the argument plainly).
func buildPrefixForkHiddenChildLanguage() *Language {
	return &Language{
		Name:               "prefix_fork_hidden",
		SymbolCount:        8,
		TokenCount:         3,
		ExternalTokenCount: 0,
		StateCount:         10,
		LargeStateCount:    0,
		FieldCount:         0,
		ProductionIDCount:  5,

		SymbolNames: []string{"EOF", "x", "y", "_hidden", "pre", "A", "B", "Top"},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "x", Visible: true, Named: true},
			{Name: "y", Visible: true, Named: true},
			{Name: "_hidden", Visible: false, Named: false},
			{Name: "pre", Visible: true, Named: true},
			{Name: "A", Visible: true, Named: true},
			{Name: "B", Visible: true, Named: true},
			{Name: "Top", Visible: true, Named: true},
		},
		FieldNames: []string{""},

		ParseActions: []ParseActionEntry{
			{Actions: nil}, // 0: error
			{Actions: []ParseAction{{Type: ParseActionShift, State: 1}}},                                   // 1: S0 col x
			{Actions: []ParseAction{{Type: ParseActionShift, State: 3}}},                                   // 2: S0 col _hidden (goto)
			{Actions: []ParseAction{{Type: ParseActionShift, State: 4}}},                                   // 3: S0 col pre (goto)
			{Actions: []ParseAction{{Type: ParseActionShift, State: 6}}},                                   // 4: S0 col A (goto)
			{Actions: []ParseAction{{Type: ParseActionShift, State: 7}}},                                   // 5: S0 col B (goto)
			{Actions: []ParseAction{{Type: ParseActionShift, State: 9}}},                                   // 6: S0 col Top (goto)
			{Actions: []ParseAction{{Type: ParseActionShift, State: 2}}},                                   // 7: S1 col x (second x)
			{Actions: []ParseAction{{Type: ParseActionReduce, Symbol: 3, ChildCount: 2, ProductionID: 0}}}, // 8: S2 reduce _hidden -> x x
			{Actions: []ParseAction{{Type: ParseActionReduce, Symbol: 4, ChildCount: 1, ProductionID: 1}}}, // 9: S3 reduce pre -> _hidden
			{Actions: []ParseAction{{Type: ParseActionShift, State: 5}}},                                   // 10: S4 col x (third x)
			{Actions: []ParseAction{ // 11: S5 col y -- GLR reduce/reduce fork
				{Type: ParseActionReduce, Symbol: 5, ChildCount: 2, ProductionID: 2, DynamicPrecedence: 0},
				{Type: ParseActionReduce, Symbol: 6, ChildCount: 2, ProductionID: 3, DynamicPrecedence: 0},
			}},
			{Actions: []ParseAction{{Type: ParseActionShift, State: 8}}},                                   // 12: S6/S7 col y
			{Actions: []ParseAction{{Type: ParseActionReduce, Symbol: 7, ChildCount: 2, ProductionID: 4}}}, // 13: S8 reduce Top -> (A|B) y
			{Actions: []ParseAction{{Type: ParseActionAccept}}},                                            // 14: S9 accept
		},

		// Columns: EOF(0), x(1), y(2), _hidden(3), pre(4), A(5), B(6), Top(7)
		ParseTable: [][]uint16{
			{0, 1, 0, 2, 3, 4, 5, 6},  // S0 start
			{0, 7, 0, 0, 0, 0, 0, 0},  // S1 saw x1
			{8, 8, 8, 0, 0, 0, 0, 0},  // S2 saw x1 x2 -> reduce _hidden
			{9, 9, 9, 0, 0, 0, 0, 0},  // S3 saw _hidden -> reduce pre
			{0, 10, 0, 0, 0, 0, 0, 0}, // S4 saw pre
			{0, 0, 11, 0, 0, 0, 0, 0}, // S5 saw pre,x3 -> FORK on y
			{0, 0, 12, 0, 0, 0, 0, 0}, // S6 saw A
			{0, 0, 12, 0, 0, 0, 0, 0}, // S7 saw B
			{13, 0, 0, 0, 0, 0, 0, 0}, // S8 saw A|B,y -> reduce Top
			{14, 0, 0, 0, 0, 0, 0, 0}, // S9 saw Top -> accept
		},

		LexModes: []LexMode{
			{LexState: 0}, {LexState: 0}, {LexState: 0}, {LexState: 0}, {LexState: 0},
			{LexState: 0}, {LexState: 0}, {LexState: 0}, {LexState: 0}, {LexState: 0},
		},

		LexStates: []LexState{
			{
				AcceptToken: 0, Skip: false, Default: -1, EOF: -1,
				Transitions: []LexTransition{
					{Lo: 'x', Hi: 'x', NextState: 1},
					{Lo: 'y', Hi: 'y', NextState: 2},
					{Lo: ' ', Hi: ' ', NextState: 3},
				},
			},
			{AcceptToken: 1, Skip: false, Default: -1, EOF: -1},
			{AcceptToken: 2, Skip: false, Default: -1, EOF: -1},
			{AcceptToken: 0, Skip: true, Default: -1, EOF: -1, Transitions: []LexTransition{{Lo: ' ', Hi: ' ', NextState: 3}}},
		},
	}
}

// TestPrefixForkHiddenChildLanguageActuallyForks instruments (via
// ParseRuntime) that "x x x y" genuinely drives more than one live GLR
// stack, and that the winning tree still shows the materialized (flattened)
// view of "pre" — proving the hidden-child construction actually took
// effect (2 materialized children under "pre", not 1).
func TestPrefixForkHiddenChildLanguageActuallyForks(t *testing.T) {
	lang := buildPrefixForkHiddenChildLanguage()
	// Pin to production: this test asserts a production-engine GSS internal
	// (MaxStacksSeen fork count) the compact candidate route does not populate.
	parser := NewParser(lang)
	parser.SetAdmissionCandidateRoute(false)
	tree := mustParse(t, parser, []byte("x x x y"))
	defer tree.Release()

	rt := tree.ParseRuntime()
	if rt.StopReason != ParseStopAccepted {
		t.Fatalf("StopReason = %v, want accepted", rt.StopReason)
	}
	if rt.MaxStacksSeen <= 1 {
		t.Fatalf("MaxStacksSeen = %d, want > 1 (test input must actually fork)", rt.MaxStacksSeen)
	}
	root := tree.RootNode()
	if got, want := root.Symbol(), Symbol(7); got != want {
		t.Fatalf("root symbol = %d, want Top(7)", got)
	}
	// The winning alternative's "pre" child must show 2 MATERIALIZED
	// children (the flattened _hidden pair), proving the pre/post-flatten
	// mismatch is real: pre's own raw shape (if captured) has exactly 1
	// child (_hidden, unflattened).
	winner := root.Child(0) // A or B
	pre := winner.Child(0)
	if got, want := pre.Symbol(), Symbol(4); got != want {
		t.Fatalf("winner's first child symbol = %d, want pre(4)", got)
	}
	if got, want := int(pre.ChildCount()), 2; got != want {
		t.Fatalf("pre materialized child count = %d, want %d (flattened _hidden pair)", got, want)
	}
}

// TestRawShapeElisionDifferentialPrefixThenForkSharedRootSymbol is the
// strengthened load-bearing differential an adversarial review asked for
// (see buildPrefixForkHiddenChildLanguage's doc comment for the full
// construction and rationale). It passes: the selected tree is identical
// gate-on vs gate-off, for a fork whose alternatives share a root symbol and
// whose shared single-stack prefix has a genuine pre/post-flatten childCount
// mismatch.
//
// This does NOT mean the mismatch is invisible to the comparison in every
// case: TestRawShapeElisionAbstractComparisonFallbackCanFlipSign proves the
// one-sided-shape fallback branch itself is not universally sign-preserving
// when fed an artificially asymmetric pair. What this test (and the
// structural argument in captureRawShape's doc comment) shows is that a real
// parse under this gate can never actually construct that asymmetric pair:
// compareRawStackEntriesRec, walking from the shared "Top" root, still
// resolves via A's vs B's own (necessarily different, that difference IS
// the fork) symbol one level up from "pre" — so "pre" is only ever compared
// against itself (symmetric hasShape, safe), never against a different
// object.
func TestRawShapeElisionDifferentialPrefixThenForkSharedRootSymbol(t *testing.T) {
	lang := buildPrefixForkHiddenChildLanguage()
	source := []byte("x x x y")

	// Pin to production: this test asserts production-engine GSS internals
	// (MaxStacksSeen fork count) the compact candidate route does not populate.
	SetRawShapeElisionDisabledForDiagnostics(false)
	gateOnParser := NewParser(lang)
	gateOnParser.SetAdmissionCandidateRoute(false)
	gateOn := mustParse(t, gateOnParser, source)
	defer gateOn.Release()
	gateOnRuntime := gateOn.ParseRuntime()

	SetRawShapeElisionDisabledForDiagnostics(true)
	defer SetRawShapeElisionDisabledForDiagnostics(false)
	gateOffParser := NewParser(lang)
	gateOffParser.SetAdmissionCandidateRoute(false)
	gateOff := mustParse(t, gateOffParser, source)
	defer gateOff.Release()
	gateOffRuntime := gateOff.ParseRuntime()

	if gateOnRuntime.MaxStacksSeen <= 1 || gateOffRuntime.MaxStacksSeen <= 1 {
		t.Fatalf("MaxStacksSeen gate-on=%d gate-off=%d, want both > 1 (fork precondition)", gateOnRuntime.MaxStacksSeen, gateOffRuntime.MaxStacksSeen)
	}
	if got, want := gateOn.RootNode().SExpr(lang), gateOff.RootNode().SExpr(lang); got != want {
		t.Fatalf("gate-on tree = %s, gate-off (elision disabled) tree = %s, want identical", got, want)
	}
}

// TestRawShapeElisionAbstractComparisonFallbackCanFlipSign directly exercises
// compareRawStackEntriesRec's one-sided-shape fallback (parser_reduce.go,
// the `aHasShape != bHasShape` branch) with a hand-constructed, artificially
// asymmetric pair: "preElided" and "preCaptured" model the SAME logical node
// in the two counterfactual worlds (elision on: no raw shape at all; elision
// off: the raw shape it would have captured, 1 child = the still-unflattened
// hidden wrapper), both compared against the same "D" (a different node,
// same symbol, always captured normally).
//
// This proves the fallback branch is NOT universally sign-preserving in
// isolation: feeding it an asymmetric pair can and does flip the comparison
// sign here. That is exactly why this optimization does not rely on "the
// fallback always preserves sign" as its safety argument. The actual
// argument (see captureRawShape's doc comment and
// TestRawShapeElisionDifferentialPrefixThenForkSharedRootSymbol) is that a
// real parse under this gate can never feed compareRawStackEntriesRec this
// exact shape of input: an elided node is always compared against ITSELF
// (same object, symmetric hasShape), never against a different object like
// "D" here — this test's "preElided vs D" pairing is a deliberately
// unreachable construction, included to answer "could the sign flip"
// directly rather than leaving it as an open question.
func TestRawShapeElisionAbstractComparisonFallbackCanFlipSign(t *testing.T) {
	arena := acquireNodeArena(arenaClassFull)
	defer arena.Release()
	parser := testRawShapeParser()

	const (
		hiddenSym Symbol = 2
		childASym Symbol = 3
		childCSym Symbol = 4
		childBSym Symbol = 5 // deliberately > childCSym
		preSym    Symbol = 1
	)

	childA := newLeafNodeInArena(arena, childASym, true, 0, 1, Point{}, Point{Column: 1})
	childB := newLeafNodeInArena(arena, childBSym, true, 1, 2, Point{Column: 1}, Point{Column: 2})
	childC := newLeafNodeInArena(arena, childCSym, true, 1, 2, Point{Column: 1}, Point{Column: 2})
	hiddenWrap := newParentNodeInArena(arena, hiddenSym, false, []*Node{childA, childB}, nil, 0)

	// preElided: materialized children flattened to [childA, childB] (mirrors
	// buildReduceChildrenWithPath splicing an invisible child's own children
	// into the parent), no raw shape at all -- simulating single-stack
	// elision.
	preElided := newParentNodeInArena(arena, preSym, true, []*Node{childA, childB}, nil, 0)

	// preCaptured: the SAME logical node in the counterfactual gate-off
	// world: identical materialized children, but WITH the raw shape it
	// would have captured (1 child: the still-unflattened hiddenWrap).
	preCaptured := newParentNodeInArena(arena, preSym, true, []*Node{childA, childB}, nil, 0)
	preCaptured.rawShape = parser.captureRawShape(nil, arena, preSym, 0, []stackEntry{newStackEntryNode(0, hiddenWrap)}, 0, 1)

	// d: the OTHER side of the comparison -- same symbol as pre, different
	// materialized children [childA, childC], captured normally (as any
	// node built after the parse's first fork always is).
	d := newParentNodeInArena(arena, preSym, true, []*Node{childA, childC}, nil, 0)
	d.rawShape = parser.captureRawShape(nil, arena, preSym, 0, []stackEntry{
		newStackEntryNode(0, childA),
		newStackEntryNode(0, childC),
	}, 0, 2)

	dEntry := newStackEntryNode(0, d)
	cmpOn := parser.compareRawStackEntries(arena, newStackEntryNode(0, preElided), dEntry)
	cmpOff := parser.compareRawStackEntries(arena, newStackEntryNode(0, preCaptured), dEntry)

	if cmpOn == 0 || cmpOff == 0 {
		t.Fatalf("expected nonzero comparisons on both sides, got cmpOn=%d cmpOff=%d", cmpOn, cmpOff)
	}
	if (cmpOn < 0) == (cmpOff < 0) {
		t.Fatalf("expected a sign flip between the elided and captured counterfactuals, got cmpOn=%d cmpOff=%d (same sign)", cmpOn, cmpOff)
	}
}
