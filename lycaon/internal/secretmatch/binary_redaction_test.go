package secretmatch

import (
	"bytes"
	"context"
	"testing"
	"unicode/utf8"
)

func TestRedactionPreservesBinaryBytesOutsideMatchedRanges(t *testing.T) {
	const secret = "synthetic-protected-value"
	prefix := []byte{0xff, 0xc3, 0x28, 0, 'b', 'e', 'f', 'o', 'r', 'e', ' '}
	suffix := []byte{' ', 0xfe, 0xf0, 0x28, 0x8c, 0x28, '\r', '\n'}
	input := append(append(append([]byte(nil), prefix...), []byte(secret)...), suffix...)
	matcher := NewInertMatcher()
	matcher.SetHarvestSource(func(context.Context) []HarvestedValue {
		return []HarvestedValue{{Secret: secret, RuleID: ManagedRuleID, NonDisclosable: true, Fingerprint: "sf1_0123456789abcdef"}}
	})
	safe, spans := matcher.RedactLabeledSpansWhere(t.Context(), "", string(input), nil)
	expected := append(append(append([]byte(nil), prefix...), []byte(RedactedMarker)...), suffix...)
	if !bytes.Equal([]byte(safe), expected) || len(spans) != 1 {
		t.Fatalf("unmatched bytes changed: got %x want %x", []byte(safe), expected)
	}
	if spans[0].Start != utf8.RuneCount(prefix) {
		t.Fatalf("marker rune offset=%d", spans[0].Start)
	}
	tagged := ApplyPseudonyms(safe, spans)
	expected = append(append(append([]byte(nil), prefix...), []byte("[REDACTED:sf1_01234567]")...), suffix...)
	if !bytes.Equal([]byte(tagged), expected) {
		t.Fatalf("pseudonym changed unmatched bytes: got %x want %x", []byte(tagged), expected)
	}
	hits := matcher.ScreenContext(t.Context(), string(input))
	byteSpans := MatchByteSpans(string(input), hits)
	if len(byteSpans) != 1 || byteSpans[0].Start != len(prefix) || byteSpans[0].End != len(prefix)+len(secret) {
		t.Fatalf("input byte spans=%+v", byteSpans)
	}
}
func TestMatchByteSpansRejectsInvalidRanges(t *testing.T) {
	hits := []Match{{Start: -1, End: 1}, {Start: 0, End: 8}, {Start: 2, End: 1}, {Start: 1, End: 3}}
	spans := MatchByteSpans("xλz", hits)
	if len(spans) != 1 || spans[0] != (ByteSpan{Start: 1, End: 4}) {
		t.Fatalf("byte ranges=%+v", spans)
	}
}
