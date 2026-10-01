//go:build !grammar_subset || grammar_subset_cobol

package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestCobolExternalLexStatesDefaultElection(t *testing.T) {
	want := [][]bool{
		{false, false, false, false, false, false},
		{true, true, true, true, true, true},
		{true, true, true, true, false, false},
		{true, true, true, true, false, true},
		{true, true, true, true, true, false},
	}

	got := LookupExternalLexStates("cobol")
	if len(got) != len(want) {
		t.Fatalf("LookupExternalLexStates(%q) rows = %d, want %d", "cobol", len(got), len(want))
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("ExternalLexStates[%d] len = %d, want %d", i, len(got[i]), len(want[i]))
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("ExternalLexStates[%d][%d] = %v, want %v", i, j, got[i][j], want[i][j])
			}
		}
	}

	lang := CobolLanguage()
	diag := gotreesitter.DiagnoseCRecoveryGate(lang)
	if !diag.Supported {
		t.Fatalf("DiagnoseCRecoveryGate rejected cobol with default ELS: %s", diag.Reason)
	}
	if !lang.CRecoveryCostCompetitionCapable || !lang.CRecoveryCostCompetitionEnabledByDefault {
		t.Fatalf("cobol C recovery election did not enable by default: capable=%v default=%v",
			lang.CRecoveryCostCompetitionCapable, lang.CRecoveryCostCompetitionEnabledByDefault)
	}
	maxELS := 0
	for _, mode := range lang.LexModes {
		if int(mode.ExternalLexState) > maxELS {
			maxELS = int(mode.ExternalLexState)
		}
	}
	if maxELS >= len(lang.ExternalLexStates) {
		t.Fatalf("LexModes reference external_lex_state %d but table only has %d rows", maxELS, len(lang.ExternalLexStates))
	}
}
