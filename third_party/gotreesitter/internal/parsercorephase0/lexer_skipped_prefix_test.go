package parsercorephase0

import "testing"

func TestLexerSkippedPrefixProvenanceIsSparseAndMaterialized(t *testing.T) {
	tables := &fakeTable{actions: map[tableCell][]Action{
		{state: 1, symbol: 9}: {{Type: ActionShift, State: 2}},
	}}
	compact, err := New(tables, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := compact.Seed(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	shifted, err := compact.Shift(seed, 9, 0, Token{
		Symbol: 9, StartByte: 5, EndByte: 6,
		LexerSkippedPrefixLength: 4,
	}, ForkOrder{})
	if err != nil {
		t.Fatal(err)
	}
	derivations, err := compact.Derivations(shifted)
	if err != nil || len(derivations) != 1 || len(derivations[0].Payloads) != 1 {
		t.Fatalf("derivations=%+v err=%v", derivations, err)
	}
	payload := derivations[0].Payloads[0]
	view, err := compact.MaterializationView(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !view.LexerSkippedPrefix || view.LexerSkippedPrefixStart != 1 {
		t.Fatalf("materialization provenance=%t/%d, want true/1", view.LexerSkippedPrefix, view.LexerSkippedPrefixStart)
	}
	if len(compact.lexerSkippedPrefixes) != 1 || compact.lexerSkippedPrefixes[0] != (lexerSkippedPrefixProvenance{payload: payload, start: 1}) {
		t.Fatalf("sparse provenance=%+v", compact.lexerSkippedPrefixes)
	}
	if err := compact.Reset(); err != nil {
		t.Fatal(err)
	}
	if len(compact.lexerSkippedPrefixes) != 0 {
		t.Fatalf("reset retained %d skipped-prefix records", len(compact.lexerSkippedPrefixes))
	}
}

func TestLexerSkippedPrefixProvenanceRollsBackWithFailedShift(t *testing.T) {
	tables := &fakeTable{actions: map[tableCell][]Action{
		{state: 1, symbol: 9}: {{Type: ActionShift, State: 2}},
	}}
	compact, err := New(tables, Limits{MaxNodes: 1, MaxLinks: 8, MaxSubtrees: 8, MaxChildren: 8, MaxMetadata: 8, MaxLinksPerBoundary: 8})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := compact.Seed(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compact.Shift(seed, 9, 0, Token{
		Symbol: 9, StartByte: 5, EndByte: 6,
		LexerSkippedPrefixLength: 4,
	}, ForkOrder{}); err == nil {
		t.Fatal("node-cap shift unexpectedly succeeded")
	}
	if len(compact.subtrees) != 0 || len(compact.lexerSkippedPrefixes) != 0 {
		t.Fatalf("failed shift retained subtrees=%d skipped_prefixes=%d", len(compact.subtrees), len(compact.lexerSkippedPrefixes))
	}
}

func TestLexerSkippedPrefixProvenanceRejectsInvalidProof(t *testing.T) {
	tests := []struct {
		name  string
		token Token
	}{
		{
			name: "external_token",
			token: Token{
				Symbol: 9, StartByte: 5, EndByte: 6, External: true,
				LexerSkippedPrefixLength: 4,
			},
		},
		{
			name: "prefix_before_source",
			token: Token{
				Symbol: 9, StartByte: 5, EndByte: 6,
				LexerSkippedPrefixLength: 6,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tables := &fakeTable{actions: map[tableCell][]Action{
				{state: 1, symbol: 9}: {{Type: ActionShift, State: 2}},
			}}
			compact, err := New(tables, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			seed, err := compact.Seed(1, 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := compact.Shift(seed, 9, 0, test.token, ForkOrder{}); err == nil {
				t.Fatal("invalid skipped-prefix proof unexpectedly succeeded")
			}
			if len(compact.subtrees) != 0 || len(compact.lexerSkippedPrefixes) != 0 {
				t.Fatalf("invalid proof retained subtrees=%d skipped_prefixes=%d", len(compact.subtrees), len(compact.lexerSkippedPrefixes))
			}
		})
	}
}
