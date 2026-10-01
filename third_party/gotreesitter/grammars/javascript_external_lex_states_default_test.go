package grammars

import "testing"

func TestJavascriptExternalLexStatesDefaultWithoutRecoveryElection(t *testing.T) {
	if got := len(LookupExternalLexStates("javascript")); got != 10 {
		t.Fatalf("LookupExternalLexStates(\"javascript\") rows = %d, want 10", got)
	}
	lang := JavascriptLanguage()
	if got := len(lang.ExternalLexStates); got != 10 {
		t.Fatalf("JavascriptLanguage ExternalLexStates rows = %d, want 10", got)
	}
	if lang.CRecoveryCostCompetitionEnabledByDefault {
		t.Fatal("javascript C recovery election default-enabled despite performance opt-out")
	}
}
