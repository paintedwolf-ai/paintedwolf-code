package grammargen

import "testing"

// TestGoGrammarAutomaticSemicolonExternalSymbol pins the external-symbol
// surface that grammars/go_scanner.go depends on: exactly one external
// token, named "_automatic_semicolon", assigned symbol ID 94. If this test
// fails after a grammar edit, regenerate grammars/grammar_blobs/go.bin via
// `go run ./cmd/grammargen emit go -bin grammars/grammar_blobs/go.bin` (NOT
// -lr-split — see grammargen/README.md: -lr-split hangs on ordinary Go
// inputs once the grammar has an external symbol) and update the
// goSymAutoSemicolon constant in grammars/go_scanner.go to match the new ID
// reported here, rather than leaving the scanner silently mis-lexing.
func TestGoGrammarAutomaticSemicolonExternalSymbol(t *testing.T) {
	g := GoGrammar()
	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage failed: %v", err)
	}
	if len(lang.ExternalSymbols) != 1 {
		t.Fatalf("ExternalSymbols = %v, want exactly 1", lang.ExternalSymbols)
	}
	sym := lang.ExternalSymbols[0]
	if int(sym) >= len(lang.SymbolNames) {
		t.Fatalf("external symbol %d out of range of SymbolNames (len=%d)", sym, len(lang.SymbolNames))
	}
	if got := lang.SymbolNames[sym]; got != "_automatic_semicolon" {
		t.Fatalf("external symbol name = %q, want %q", got, "_automatic_semicolon")
	}
	const wantSymbolID = 94
	if int(sym) != wantSymbolID {
		t.Fatalf("_automatic_semicolon symbol ID = %d, want %d (update goSymAutoSemicolon in grammars/go_scanner.go to match)", sym, wantSymbolID)
	}
	if lang.ExternalScanner != nil {
		t.Fatal("grammargen.GenerateLanguage should not itself attach a runtime scanner; that happens in the grammars package")
	}
	if len(lang.ExternalLexStates) == 0 {
		t.Fatal("external generated language was certified without ExternalLexStates")
	}
}
