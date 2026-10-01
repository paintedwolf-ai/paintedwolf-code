//go:build gts_parsercorephase0 && !gts_no_parsercorephase0

package gotreesitter_test

import (
	"testing"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestDiagnosticParserCoreVersionLexerRequestsOwnRaggedSpans(t *testing.T) {
	t.Cleanup(func() { grammars.PurgeEmbeddedLanguageCache() })
	entry := grammars.DetectLanguageByName("swift")
	if entry == nil || entry.Language() == nil {
		t.Fatal("swift grammar is unavailable")
	}
	requests, err := gts.DiagnosticParserCoreVersionLexerRequestWitnessForTest(
		entry.Language(), []byte("a<A<#"),
	)
	if err != nil {
		t.Fatalf("version lexer request witness: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("version lexer requests=%d, want 2", len(requests))
	}
	want := map[gts.StateID]struct {
		symbol   gts.Symbol
		endByte  uint32
		external bool
		internal bool
	}{
		47:  {symbol: 35, endByte: 4, internal: true},
		524: {symbol: 217, endByte: 5, external: true},
	}
	for _, request := range requests {
		expect, ok := want[request.State]
		if !ok {
			t.Fatalf("unexpected request state=%d", request.State)
		}
		if request.Token.Symbol != expect.symbol || request.Token.StartByte != 3 || request.Token.EndByte != expect.endByte {
			t.Fatalf("state %d token=%+v, want symbol=%d span=3..%d", request.State, request.Token, expect.symbol, expect.endByte)
		}
		if request.Token.ExternalScannerToken != expect.external {
			t.Fatalf("state %d external=%t, want %t", request.State, request.Token.ExternalScannerToken, expect.external)
		}
		if request.InternalDFAToken != expect.internal {
			t.Fatalf("state %d internal DFA=%t, want %t", request.State, request.InternalDFAToken, expect.internal)
		}
		if request.ScannerBefore.Length != 9 || request.ScannerAfter.Length != 9 {
			t.Fatalf("state %d lost scanner checkpoint pair: before=%+v after=%+v", request.State, request.ScannerBefore, request.ScannerAfter)
		}
	}
}
