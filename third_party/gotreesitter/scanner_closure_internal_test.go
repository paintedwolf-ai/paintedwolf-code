package gotreesitter

import "testing"

type scannerClosureFallbackSpy struct {
	skips int
}

func (*scannerClosureFallbackSpy) Next() Token               { return Token{} }
func (*scannerClosureFallbackSpy) Close()                    {}
func (s *scannerClosureFallbackSpy) SkipToByte(uint32) Token { s.skips++; return Token{Symbol: 1} }
func (s *scannerClosureFallbackSpy) SkipToByteWithPoint(uint32, Point) Token {
	s.skips++
	return Token{Symbol: 1}
}

// TestCompactLeafRequiredCheckpointDoesNotUseGenericFallback is the P0 closure
// assertion: when checkpoint proof is mandatory, an unsupported/missing
// checkpoint fails immediately even if the token source offers generic skip
// methods that could otherwise manufacture a matching leaf token.
func TestCompactLeafRequiredCheckpointDoesNotUseGenericFallback(t *testing.T) {
	spy := &scannerClosureFallbackSpy{}
	leaf := &Node{symbol: 1, startByte: 4, endByte: 5, preGotoState: 2}
	if tok, ok := scanCompactLeafWithRequiredScannerCheckpoint(spy, leaf); ok || tok != (Token{}) {
		t.Fatalf("required-checkpoint scan = (%+v, %v), want zero/false", tok, ok)
	}
	if spy.skips != 0 {
		t.Fatalf("required-checkpoint scan used generic fallback %d times", spy.skips)
	}
}
