package gotreesitter

// Campaign O(edit) workstream W1 (spec.campaign.oedit): fragility-gated
// top-level sibling block-splice. These tests cover the check-items a prior
// review flagged for this class of change:
//
//   - topLevelSiblingBlockSpliceEligible admits a top-level sibling on ANY
//     language (not just the retired cmake/css
//     languageAllowsForestTopLevelSiblingReuse allowlist) via the cheap
//     primary-loop reuseTargetState check, proving the allowlist is
//     genuinely subsumed rather than merely bypassed for Go specifically.
//   - A fragile candidate at the exact same position is barred from that
//     fast path (TestTopLevelSiblingBlockSpliceRejectsFragileCandidate).
//
// The real-parse, end-to-end counterpart proving the fragility gate cannot
// be silently neutered by a real GLR merge/dispatch path lives in
// w1_sibling_splice_realparse_test.go (package gotreesitter_test -- it needs
// the grammars package, which would be an import cycle from here).

import "testing"

// buildTopLevelListLanguage constructs a minimal hand-built LR grammar for a
// flat list of single-token statements, deliberately shaped like the
// "canonical list-continuation state" every source_file-style grammar (Go's
// included) relies on for top-level sibling splicing:
//
//	program -> stmt*
//	stmt    -> ITEM
//
// Symbols: 0 EOF, 1 ITEM (terminal), 2 stmt (nonterminal).
// States 0 and 1 are both "ready for the next stmt" (0 before any stmt has
// been seen, 1 the self-loop reached after reducing one) -- kept distinct,
// rather than collapsed to a single state, because state 0 can never be
// returned as a GOTO target through this table encoding (lookupGoto treats a
// raw 0 result as "no entry"; see its doc comment on hand-built-grammar
// convention, parser_tables.go), so the reusable list-continuation state
// needs a nonzero id. State 2 always reduces stmt -> ITEM regardless of
// lookahead.
func buildTopLevelListLanguage() *Language {
	return &Language{
		Name:               "w1_toplevel_list",
		SymbolCount:        3,
		TokenCount:         2,
		ExternalTokenCount: 0,
		StateCount:         3,
		ProductionIDCount:  1,

		SymbolNames: []string{"EOF", "ITEM", "stmt"},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "ITEM", Visible: true, Named: true},
			{Name: "stmt", Visible: true, Named: true},
		},
		FieldNames: []string{""},

		ParseActions: []ParseActionEntry{
			// 0: no-op / error
			{Actions: nil},
			// 1: shift ITEM -> state 2
			{Actions: []ParseAction{{Type: ParseActionShift, State: 2}}},
			// 2: goto stmt -> state 1 (canonical list-continuation state)
			{Actions: []ParseAction{{Type: ParseActionShift, State: 1}}},
			// 3: accept on EOF
			{Actions: []ParseAction{{Type: ParseActionAccept}}},
			// 4: reduce stmt -> ITEM (1 child), unconditional
			{Actions: []ParseAction{{Type: ParseActionReduce, Symbol: 2, ChildCount: 1, ProductionID: 0}}},
		},

		// Columns: EOF(0), ITEM(1), stmt(2)
		ParseTable: [][]uint16{
			{3, 1, 2}, // state 0: start
			{3, 1, 2}, // state 1: list-continuation self-loop
			{4, 4, 4}, // state 2: saw ITEM, reduce unconditionally
		},

		LexModes: []LexMode{{LexState: 0}, {LexState: 0}, {LexState: 0}},
		LexStates: []LexState{
			{
				AcceptToken: 0,
				Default:     -1,
				EOF:         -1,
				Transitions: []LexTransition{{Lo: 'a', Hi: 'z', NextState: 1}},
			},
			{
				AcceptToken: 1,
				Default:     -1,
				EOF:         -1,
				Transitions: []LexTransition{{Lo: 'a', Hi: 'z', NextState: 1}},
			},
		},
	}
}

// topLevelSpliceFixture builds a 3-statement old tree (root -> stmt0, stmt1,
// stmt2, each stmt -> one ITEM leaf) with a same-length edit inside stmt0, so
// reuseCursor.reset's topLevelParent bookkeeping treats stmt1/stmt2 as the
// trailing unaffected sibling run (topLevelResumeByte == stmt0.EndByte()).
// fragile marks stmt1's fragility bits, mirroring markReduceFragility's
// output for a reduce that ran under GLR ambiguity.
func topLevelSpliceFixture(t *testing.T, fragile, stateless bool) (*Parser, *reuseCursor, []byte) {
	t.Helper()
	lang := buildTopLevelListLanguage()
	if stateless {
		lang.ExternalScanner = quiescenceStatelessScanner{}
	}
	parser := NewParser(lang)

	// 6 bytes: "aa" (stmt0) + "bb" (stmt1) + "cc" (stmt2).
	oldSource := []byte("aabbcc")
	newSource := []byte("xxbbcc") // stmt0 edited in place; stmt1/stmt2 unchanged.

	mkStmt := func(start uint32) *Node {
		leaf := NewLeafNode(1, true, start, start+2, Point{Row: 0, Column: start}, Point{Row: 0, Column: start + 2})
		stmt := NewParentNode(2, true, []*Node{leaf}, nil, 0)
		stmt.parseState = 0
		return stmt
	}
	stmt0 := mkStmt(0)
	stmt1 := mkStmt(2)
	stmt2 := mkStmt(4)
	if fragile {
		stmt1.setFragileLeft(true)
		stmt1.setFragileRight(true)
	}
	root := NewParentNode(3, true, []*Node{stmt0, stmt1, stmt2}, nil, 0)

	oldTree := NewTree(root, oldSource, lang)
	oldTree.Edit(InputEdit{
		StartByte: 0, OldEndByte: 2, NewEndByte: 2,
		StartPoint:  Point{Row: 0, Column: 0},
		OldEndPoint: Point{Row: 0, Column: 2},
		NewEndPoint: Point{Row: 0, Column: 2},
	})

	var reuseScratch reuseScratch
	reuse := (&reuseCursor{}).reset(oldTree, newSource, &reuseScratch)
	if reuse == nil {
		t.Fatal("reuse cursor reset returned nil")
	}
	if reuse.topLevelParent == nil {
		t.Fatal("fixture invariant: expected reset to identify a top-level parent boundary")
	}
	if reuse.topLevelResumeByte != stmt0.EndByte() {
		t.Fatalf("fixture invariant: topLevelResumeByte = %d, want %d", reuse.topLevelResumeByte, stmt0.EndByte())
	}
	return parser, reuse, newSource
}

// TestTopLevelSiblingBlockSpliceAdmitsNonForestNonCSSLanguage proves the W1
// generalization: a top-level sibling on a language that is NOT cmake/css
// and whose old tree was NOT built by the GSS-forest fast path (forestFastPath
// is false here) is still admitted through the cheap primary-loop
// reuseTargetState check, via topLevelSiblingBlockSpliceEligible alone.
// Before W1 this fixture would have been rejected outright by
// directForestTopLevelNonLeafReuse's forestFastPath+language-allowlist gate
// and would have had to fall through to the conservative per-node fallback
// lane (or fail entirely, as the retired allowlist gate would have refused
// it before ever trying).
func TestTopLevelSiblingBlockSpliceAdmitsNonForestNonCSSLanguage(t *testing.T) {
	parser, reuse, newSource := topLevelSpliceFixture(t, false, false)
	if reuse.forestFastPath {
		t.Fatal("fixture invariant: forestFastPath must be false to prove the allowlist gate is gone")
	}
	if reuse.languageName == "cmake" || reuse.languageName == "css" {
		t.Fatalf("fixture invariant: languageName=%q must not be cmake/css", reuse.languageName)
	}

	var entryScratch glrEntryScratch
	var gssScratch gssScratch
	// State 1 is buildTopLevelListLanguage's list-continuation state: exactly
	// where a fresh dispatch would already be after reducing stmt0, so this
	// exercises the primary (cheap) admission path, not the settling gap the
	// PR description documents for Go's real grammar.
	stack := newGLRStackWithScratch(1, &entryScratch)
	stack.byteOffset = 2 // matches stmt1's start byte: zero attachment gap.

	lookahead := Token{Symbol: 2, StartByte: 2, EndByte: 2}
	ts := &stubTokenSource{tokens: []Token{{Symbol: 0, StartByte: uint32(len(newSource)), EndByte: uint32(len(newSource))}}}

	nextTok, reusedBytes, ok := parser.tryReuseSubtree(&stack, lookahead, ts, reuse, &entryScratch, &gssScratch)
	if !ok {
		t.Fatalf("expected the non-fragile trailing sibling to be spliced via the primary loop; nextTok=%+v", nextTok)
	}
	if reusedBytes != 2 {
		t.Fatalf("reusedBytes = %d, want 2 (stmt1's span)", reusedBytes)
	}
	if reuse.rejectRootNonLeafChanged != 0 {
		t.Fatalf("rejectRootNonLeafChanged = %d, want 0 -- this candidate must be admitted by the primary loop, not rejected", reuse.rejectRootNonLeafChanged)
	}
	if reuse.rejectFragileNonLeaf != 0 {
		t.Fatalf("rejectFragileNonLeaf = %d, want 0 -- a non-fragile candidate must not trip the fragility counter", reuse.rejectFragileNonLeaf)
	}
}

// TestTopLevelSiblingBlockSpliceRequiresOwnershipForStatelessAdmission locks
// down the ownership half of the stateless-scanner proof. Scanner quiescence
// alone is insufficient: a whole old reduction may have a compatible goto
// from the live state without having been reduced from that state. Newly
// certified stateless scanners therefore reject this fabricated mismatch,
// while the preceding legacy-lane test preserves the measured W1 contract.
func TestTopLevelSiblingBlockSpliceRequiresOwnershipForStatelessAdmission(t *testing.T) {
	parser, reuse, newSource := topLevelSpliceFixture(t, false, true)
	if !reuse.strictTopLevelOwnership {
		t.Fatal("stateless scanner did not enable strict top-level ownership")
	}

	var entryScratch glrEntryScratch
	var gssScratch gssScratch
	stack := newGLRStackWithScratch(1, &entryScratch)
	stack.byteOffset = 2
	lookahead := Token{Symbol: 2, StartByte: 2, EndByte: 2}
	ts := &stubTokenSource{tokens: []Token{{Symbol: 0, StartByte: uint32(len(newSource)), EndByte: uint32(len(newSource))}}}

	if _, _, ok := parser.tryReuseSubtree(&stack, lookahead, ts, reuse, &entryScratch, &gssScratch); ok {
		t.Fatal("stateless admission reused a top-level node owned by a different pre-goto state")
	}
	if reuse.observedPreGotoStateMismatch == 0 {
		t.Fatal("expected the ownership mismatch to be observed")
	}
}

// TestTopLevelSiblingBlockSpliceRejectsFragileCandidate is the fixture-level
// control: the identical position, but stmt1 carries the fragility bits a
// real reduce-under-ambiguity would set. The fragility gate must bar it from
// the primary loop (ReuseRejectRootNonLeafChanged increments, since it is
// not eligible for the fast path) and the conservative fallback loop must
// also refuse it (rejectFragileNonLeaf increments), so tryReuseSubtree fails
// end to end -- proving the W1 admission gate cannot be tricked into
// splicing a fragile subtree just because it sits at a top-level boundary
// with byte-identical text.
func TestTopLevelSiblingBlockSpliceRejectsFragileCandidate(t *testing.T) {
	parser, reuse, newSource := topLevelSpliceFixture(t, true, false)

	var entryScratch glrEntryScratch
	var gssScratch gssScratch
	stack := newGLRStackWithScratch(1, &entryScratch)
	stack.byteOffset = 2

	lookahead := Token{Symbol: 2, StartByte: 2, EndByte: 2}
	ts := &stubTokenSource{tokens: []Token{{Symbol: 0, StartByte: uint32(len(newSource)), EndByte: uint32(len(newSource))}}}

	_, _, ok := parser.tryReuseSubtree(&stack, lookahead, ts, reuse, &entryScratch, &gssScratch)
	if ok {
		t.Fatal("expected the fragile trailing sibling to be rejected, not spliced")
	}
	if reuse.rejectRootNonLeafChanged == 0 {
		t.Fatal("expected rejectRootNonLeafChanged to record the primary-loop admission refusal")
	}
	// The conservative fallback also independently rejects it on fragility
	// grounds (its own, separately-reviewed isFragile() gate -- see
	// TestTryReuseSubtreeSkipsFragileNonLeafCandidate). span=2 is well under
	// maxNonLeafReuseSpan and stmt1.parent is non-nil, so it reaches that
	// check rather than being skipped for an unrelated reason.
	if reuse.rejectFragileNonLeaf == 0 {
		t.Fatal("expected the conservative fallback's independent fragility gate (rejectFragileNonLeaf) to also fire")
	}
}
