//go:build gts_parsercorephase0 && !gts_no_parsercorephase0

package gotreesitter_test

import (
	"fmt"
	"testing"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

const versionLexerProductionSource = "a<(A<#/x/#)"

// TestDiagnosticParserCoreVersionLexerProductionActivation proves that the
// fresh-full scheduler owns each lexer cursor after a width disagreement. The
// shared pass consumes '<#' at byte 4. Both retained versions consume '<' from
// their own cursors. One lineage then shifts '#' and reaches EOF acceptance.
func TestDiagnosticParserCoreVersionLexerProductionActivation(t *testing.T) {
	t.Cleanup(func() { grammars.PurgeEmbeddedLanguageCache() })
	entry := grammars.DetectLanguageByName("swift")
	if entry == nil || entry.Language() == nil {
		t.Fatal("swift grammar is unavailable")
	}
	receipt, err := gts.DiagnosticParserCoreVersionLexerProductionActivationForTest(
		entry.Language(), []byte(versionLexerProductionSource),
	)
	if err != nil {
		t.Fatalf("production scheduler: %v", err)
	}
	if receipt.Acceptance == nil {
		t.Fatalf("owned lexer route did not accept: stop=%+v", receipt.Stop)
	}
	if receipt.Stop.Detail != "" {
		t.Fatalf("accepted scheduler retained stop detail %q", receipt.Stop.Detail)
	}
	work := receipt.Acceptance.Work
	if receipt.PerVersionLexRequests != 4 || receipt.PerVersionLexRestores != 4 ||
		receipt.PerVersionLexPublications != 14 || receipt.PerVersionLexAcceptedRaggedSpans != 3 ||
		receipt.PerVersionLexViabilityDrops != 1 {
		t.Fatalf("published lexer counters=%d/%d/%d/%d/%d, want 4/4/14/3/1",
			receipt.PerVersionLexRequests, receipt.PerVersionLexRestores,
			receipt.PerVersionLexPublications, receipt.PerVersionLexAcceptedRaggedSpans,
			receipt.PerVersionLexViabilityDrops)
	}
	if work.PerVersionLexRequests != 4 || work.PerVersionLexRestores != 4 ||
		work.PerVersionLexPublications != 14 || work.PerVersionLexAcceptedRaggedSpans != 3 ||
		work.PerVersionLexViabilityDrops != 1 {
		t.Fatalf("acceptance lexer counters=%d/%d/%d/%d/%d, want 4/4/14/3/1",
			work.PerVersionLexRequests, work.PerVersionLexRestores,
			work.PerVersionLexPublications, work.PerVersionLexAcceptedRaggedSpans,
			work.PerVersionLexViabilityDrops)
	}
	if receipt.PeakLiveVersions != 2 || work.PeakLiveVersions != 2 {
		t.Fatalf("peak live versions=%d/%d, want 2/2", receipt.PeakLiveVersions, work.PeakLiveVersions)
	}
	if work.NoActionDrops != 1 || work.ConvergedReductionSplitDrops != 0 || work.ConvergedCoverageDrops != 0 {
		t.Fatalf("drop classes=%d/%d/%d, want viability-only 1/0/0",
			work.NoActionDrops, work.ConvergedReductionSplitDrops, work.ConvergedCoverageDrops)
	}

	if len(receipt.Elections) < 5 {
		t.Fatalf("elections=%d, want activation election", len(receipt.Elections))
	}
	activation := receipt.Elections[4]
	if len(activation.States) != 1 || activation.States[0] != 10 {
		t.Fatalf("activation states=%v, want [10]", activation.States)
	}
	if activation.Token.Symbol != 217 || activation.Token.StartByte != 4 ||
		activation.Token.EndByte != 6 || !activation.Token.ExternalScannerToken {
		t.Fatalf("activation token=%+v, want external 217 over bytes 4..6", activation.Token)
	}
	assertNineByteCheckpoint := func(label string, checkpoint gts.DiagnosticParserCoreScannerCheckpoint) {
		t.Helper()
		if checkpoint.Length != 9 || checkpoint.SHA256 == [32]byte{} {
			t.Fatalf("%s checkpoint=%+v, want authenticated Swift state", label, checkpoint)
		}
	}
	assertNineByteCheckpoint("activation before", activation.ScannerBefore)
	assertNineByteCheckpoint("activation after", activation.ScannerAfter)

	want := map[gts.StateID]struct {
		election      int
		symbol        gts.Symbol
		start, end    uint32
		external, dfa bool
	}{
		441:  {election: 4, symbol: 35, start: 4, end: 5, dfa: true},
		1554: {election: 4, symbol: 35, start: 4, end: 5, dfa: true},
		295:  {election: 5, symbol: 217, start: 5, end: 6, external: true},
		402:  {election: 5, symbol: gts.Symbol(65535), start: 5, end: 6},
	}
	if len(receipt.VersionLexerRequests) != len(want) {
		t.Fatalf("owned lexer requests=%d, want %d", len(receipt.VersionLexerRequests), len(want))
	}
	for _, request := range receipt.VersionLexerRequests {
		expect, ok := want[request.State]
		if !ok {
			t.Fatalf("unexpected owned request state=%d: requests=%+v", request.State, receipt.VersionLexerRequests)
		}
		if request.ElectionIndex != expect.election || request.Token.Symbol != expect.symbol ||
			request.Token.StartByte != expect.start || request.Token.EndByte != expect.end ||
			request.Token.ExternalScannerToken != expect.external || request.InternalDFAToken != expect.dfa {
			t.Fatalf("state %d request=%+v, want election=%d symbol=%d span=%d..%d external=%t dfa=%t",
				request.State, request, expect.election, expect.symbol, expect.start, expect.end,
				expect.external, expect.dfa)
		}
		assertNineByteCheckpoint(fmt.Sprintf("state %d before", request.State), request.ScannerBefore)
		assertNineByteCheckpoint(fmt.Sprintf("state %d after", request.State), request.ScannerAfter)
	}
	if len(receipt.NoActionDrops) != 1 {
		t.Fatalf("owned viability drops=%d, want 1", len(receipt.NoActionDrops))
	}
	if got := receipt.NoActionDrops[0].Token; got.Symbol != gts.Symbol(65535) || got.StartByte != 5 || got.EndByte != 6 {
		t.Fatalf("viability drop token=%+v, want error symbol at 5..6", got)
	}

	acceptance := receipt.Acceptance
	wantEOF := uint32(len(versionLexerProductionSource))
	if acceptance.Token.Symbol != 0 || acceptance.Token.StartByte != wantEOF || acceptance.Token.EndByte != wantEOF {
		t.Fatalf("accepted token=%+v, want EOF at byte %d", acceptance.Token, wantEOF)
	}
	if !acceptance.Header.Header.Accepted || acceptance.Header.Header.ByteOffset != wantEOF ||
		acceptance.Header.Header.Shifted || acceptance.Header.Header.Paused {
		t.Fatalf("accepted header=%+v, want sole closed EOF header", acceptance.Header.Header)
	}
}
