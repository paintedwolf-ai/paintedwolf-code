package gotreesitter

import "testing"

// The cases below pin canReuseFirstLeaf to C's rule order in
// ts_parser__can_reuse_first_leaf (parser.c:472-505), one case per clause.
// State rows: 0 and 1 share the compared mode identity; 2 differs in lex
// state; 3 is the non-terminal-extra sentinel; 4 has an external lex
// state; 5 differs only in the reserved word set; 6 differs only in the
// after-whitespace lex state, this engine's own extension to C's
// lexer-mode triple (lexerModeTriple's doc comment).
func tokenReuseTestLanguage() *Language {
	return &Language{
		KeywordCaptureToken: 7,
		LexModes: []LexMode{
			{LexState: 1},
			{LexState: 1},
			{LexState: 2},
			{LexState: ^uint16(0)},
			{LexState: 1, ExternalLexState: 5},
			{LexState: 1, ReservedWordSetID: 9},
			{LexState: 1, AfterWhitespaceLexState: 3},
		},
	}
}

func TestCanReuseFirstLeafRuleTable(t *testing.T) {
	lang := tokenReuseTestLanguage()
	withActions := ParseActionEntry{Actions: make([]ParseAction, 1)}
	withActionsReusable := ParseActionEntry{Reusable: true, Actions: make([]ParseAction, 1)}
	emptyReusable := ParseActionEntry{Reusable: true}
	empty := ParseActionEntry{}

	for _, test := range []struct {
		name  string
		state StateID
		leaf  tokenReuseLeaf
		entry ParseActionEntry
		want  bool
	}{
		{
			// parser.c:487.
			name:  "sentinel_state_never_reuses",
			state: 3,
			leaf:  tokenReuseLeaf{Symbol: 9, LeafState: 3, ParseState: 3, SizeBytes: 2},
			entry: withActionsReusable,
			want:  false,
		},
		{
			// parser.c:490-497, the same-lookahead fast path. The reusable
			// bit plays no part here.
			name:  "equal_modes_with_actions_reuses",
			state: 0,
			leaf:  tokenReuseLeaf{Symbol: 9, LeafState: 1, ParseState: 1, SizeBytes: 3},
			entry: withActions,
			want:  true,
		},
		{
			// An empty action row skips the fast path; parser.c:504 then
			// requires the reusable bit.
			name:  "equal_modes_without_actions_needs_reusable_bit",
			state: 0,
			leaf:  tokenReuseLeaf{Symbol: 9, LeafState: 1, ParseState: 1, SizeBytes: 3},
			entry: emptyReusable,
			want:  true,
		},
		{
			name:  "equal_modes_without_actions_and_without_bit_declines",
			state: 0,
			leaf:  tokenReuseLeaf{Symbol: 9, LeafState: 1, ParseState: 1, SizeBytes: 3},
			entry: empty,
			want:  false,
		},
		{
			// parser.c:494-496 -- an adopted keyword blocks the fast path;
			// parser.c:504 then declines without the bit.
			name:  "adopted_keyword_blocks_fast_path",
			state: 0,
			leaf:  tokenReuseLeaf{Symbol: 7, LeafState: 1, ParseState: 0, SizeBytes: 3, IsKeyword: true},
			entry: withActions,
			want:  false,
		},
		{
			name:  "adopted_keyword_still_reusable_through_the_bit",
			state: 0,
			leaf:  tokenReuseLeaf{Symbol: 7, LeafState: 1, ParseState: 0, SizeBytes: 3, IsKeyword: true},
			entry: withActionsReusable,
			want:  true,
		},
		{
			// parser.c:495-496 -- a non-adopted capture token from the same
			// parse state keeps the fast path.
			name:  "capture_token_same_state_reuses",
			state: 0,
			leaf:  tokenReuseLeaf{Symbol: 7, LeafState: 1, ParseState: 0, SizeBytes: 3},
			entry: withActions,
			want:  true,
		},
		{
			// A capture token from another parse state loses the fast path
			// and falls to the reusable bit.
			name:  "capture_token_other_state_falls_to_bit",
			state: 0,
			leaf:  tokenReuseLeaf{Symbol: 7, LeafState: 1, ParseState: 2, SizeBytes: 3},
			entry: withActions,
			want:  false,
		},
		{
			// parser.c:500 -- zero-width non-EOF under a different
			// lookahead set, even with the bit set.
			name:  "zero_width_non_eof_never_crosses",
			state: 0,
			leaf:  tokenReuseLeaf{Symbol: 9, LeafState: 2, ParseState: 2, SizeBytes: 0},
			entry: withActionsReusable,
			want:  false,
		},
		{
			// Zero-width end-of-file falls through to parser.c:504.
			name:  "zero_width_eof_falls_to_bit",
			state: 0,
			leaf:  tokenReuseLeaf{Symbol: 0, LeafState: 2, ParseState: 2, SizeBytes: 0},
			entry: emptyReusable,
			want:  true,
		},
		{
			// parser.c:504 -- an external lex state blocks the bit.
			name:  "external_lex_state_blocks_bit",
			state: 4,
			leaf:  tokenReuseLeaf{Symbol: 9, LeafState: 2, ParseState: 2, SizeBytes: 3},
			entry: withActionsReusable,
			want:  false,
		},
		{
			// The C triple includes the reserved word set: states 0 and 5
			// differ only there, so the fast path must not fire.
			name:  "reserved_word_set_breaks_mode_equality",
			state: 5,
			leaf:  tokenReuseLeaf{Symbol: 9, LeafState: 0, ParseState: 0, SizeBytes: 3},
			entry: withActions,
			want:  false,
		},
		{
			// The compared mode identity extends C's triple with the
			// after-whitespace lex state (lexerModeTriple's doc comment,
			// grammargen/assemble.go:138-142): states 0 and 6 differ only
			// there, so the fast path must not fire.
			name:  "after_whitespace_lex_state_breaks_mode_equality",
			state: 6,
			leaf:  tokenReuseLeaf{Symbol: 9, LeafState: 0, ParseState: 0, SizeBytes: 3},
			entry: withActions,
			want:  false,
		},
		{
			// F11 pin: parser.c:487 reads the CURRENT state's lex mode only.
			// A sentinel on the LEAF's state (state 3) must not trip that
			// rule; it only breaks the leafMode == currentMode fast path,
			// same as any other mode mismatch, and the reusable bit still
			// carries the result to true.
			name:  "leaf_state_sentinel_does_not_trigger_current_state_rule",
			state: 0,
			leaf:  tokenReuseLeaf{Symbol: 9, LeafState: 3, ParseState: 0, SizeBytes: 3},
			entry: withActionsReusable,
			want:  true,
		},
		{
			// F11 pin: a non-nil but empty Actions slice must skip the fast
			// path exactly like a nil one (the clause is action_count > 0,
			// not Actions != nil), then decline without the reusable bit.
			name:  "non_nil_empty_actions_skips_fast_path_without_bit",
			state: 0,
			leaf:  tokenReuseLeaf{Symbol: 9, LeafState: 1, ParseState: 1, SizeBytes: 3},
			entry: ParseActionEntry{Actions: []ParseAction{}},
			want:  false,
		},
		{
			// F11 pin: the same non-nil-but-empty Actions shape, now with
			// the reusable bit set, must reach parser.c:504 and reuse.
			name:  "non_nil_empty_actions_reaches_bit_clause",
			state: 0,
			leaf:  tokenReuseLeaf{Symbol: 9, LeafState: 1, ParseState: 1, SizeBytes: 3},
			entry: ParseActionEntry{Reusable: true, Actions: []ParseAction{}},
			want:  true,
		},
		{
			name:  "missing_mode_row_fails_closed",
			state: 9,
			leaf:  tokenReuseLeaf{Symbol: 9, LeafState: 0, ParseState: 0, SizeBytes: 3},
			entry: withActionsReusable,
			want:  false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := canReuseFirstLeaf(lang, test.state, test.leaf, test.entry); got != test.want {
				t.Fatalf("canReuseFirstLeaf=%v, want %v", got, test.want)
			}
		})
	}
}
