package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

// TestJavascriptExternalLexStatesRegression guards the default javascript
// ExternalLexStates table against drift from the pinned
// tree-sitter-javascript parser.c (ts_external_scanner_states[10][8]).
func TestJavascriptExternalLexStatesRegression(t *testing.T) {
	lang := JavascriptLanguage()

	// External token indices, matching lang.ExternalSymbols order (== C enum order):
	//	0 _automatic_semicolon  4 ||
	//	1 _template_chars       5 escape_sequence
	//	2 _ternary_qmark        6 regex_pattern
	//	3 html_comment          7 jsx_text
	want := [][]bool{
		/* 0 */ {false, false, false, false, false, false, false, false},
		/* 1 */ {true, true, true, true, true, true, false, true},
		/* 2 */ {false, false, false, true, false, false, false, false},
		/* 3 */ {true, false, true, true, true, false, false, false},
		/* 4 */ {false, false, true, true, true, false, false, false},
		/* 5 */ {true, false, false, true, false, false, false, false},
		/* 6 */ {false, false, false, true, false, false, false, true},
		/* 7 */ {false, true, false, true, false, true, false, false},
		/* 8 */ {false, false, false, true, false, true, false, false},
		/* 9 */ {false, false, false, true, false, false, true, false},
	}

	if got := len(lang.ExternalLexStates); got != len(want) {
		t.Fatalf("javascript ExternalLexStates rows = %d, want %d", got, len(want))
	}
	for i, wantRow := range want {
		gotRow := lang.ExternalLexStates[i]
		if len(gotRow) != len(wantRow) {
			t.Fatalf("ExternalLexStates[%d] len = %d, want %d", i, len(gotRow), len(wantRow))
		}
		for j := range wantRow {
			if gotRow[j] != wantRow[j] {
				t.Fatalf("ExternalLexStates[%d][%d] = %v, want %v (row must match tree-sitter-javascript ts_external_scanner_states)", i, j, gotRow[j], wantRow[j])
			}
		}
	}

	maxELS := 0
	for _, lm := range lang.LexModes {
		if int(lm.ExternalLexState) > maxELS {
			maxELS = int(lm.ExternalLexState)
		}
	}
	if maxELS >= len(lang.ExternalLexStates) {
		t.Fatalf("LexModes reference external_lex_state %d but table only has %d rows", maxELS, len(lang.ExternalLexStates))
	}

	// The table makes the C-recovery surface valid, but JavaScript remains an
	// explicit default opt-out because its clean large-file path regresses.
	diag := gotreesitter.DiagnoseCRecoveryGate(lang)
	if !diag.Supported {
		t.Fatalf("DiagnoseCRecoveryGate not supported with precise ELS: %s", diag.Reason)
	}
	if lang.CRecoveryCostCompetitionEnabledByDefault {
		t.Fatal("javascript C recovery default-enabled despite performance opt-out")
	}
}
