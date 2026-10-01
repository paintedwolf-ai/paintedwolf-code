package secretspan

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

func TestProposeTrimsOneQuoteLayerAndReportsIt(t *testing.T) {
	text := `  token: "pw-relay-7QK4-2ZB9-XM31",`
	start, end := 9, 34 // the quoted literal including both quotes
	got := Propose(text, start, end, true)
	if !got.Eligible {
		t.Fatalf("expected an eligible candidate, got reason %q", got.Reason)
	}
	if value := string([]rune(text)[got.Start:got.End]); value != "pw-relay-7QK4-2ZB9-XM31" {
		t.Fatalf("captured %q, want the unquoted value", value)
	}
	if got.TrimmedLeading != 1 || got.TrimmedTrailing != 1 {
		t.Fatalf("trim counts = %d/%d, want 1/1", got.TrimmedLeading, got.TrimmedTrailing)
	}
	if got.Shape == "" {
		t.Fatal("expected a shape receipt")
	}
}

func TestProposeWithoutTrimKeepsTheSelectionExactly(t *testing.T) {
	text := `"pw-relay-7QK4-2ZB9-XM31"`
	got := Propose(text, 0, 25, false)
	if !got.Eligible {
		t.Fatalf("expected eligible, got %q", got.Reason)
	}
	if got.Start != 0 || got.End != 25 {
		t.Fatalf("range = %d..%d, want the untrimmed selection", got.Start, got.End)
	}
	if got.TrimmedLeading != 0 || got.TrimmedTrailing != 0 {
		t.Fatal("no-trim must report no trimming")
	}
}

func TestProposeTreatsSelectedReferenceSyntaxAsLiteralBytes(t *testing.T) {
	text := "authorization: {{paintedwolf-secret:11111111-2222-3333-4444-555555555555}}"
	got := Propose(text, 15, len([]rune(text)), true)
	if !got.Eligible || got.Reason != "" {
		t.Fatalf("reason = %q eligible = %v, want literal bytes eligible", got.Reason, got.Eligible)
	}
}

// Preview and creation use the same screening minimum.
func TestProposeRefusesAValueTheScreenCouldNeverRecognize(t *testing.T) {
	got := Propose("abc", 0, 3, true)
	if got.Eligible || got.Reason != IneligibleTooShort {
		t.Fatalf("reason = %q eligible = %v, want a too-short refusal", got.Reason, got.Eligible)
	}
}

// The preview floor is the mint floor, counted the same way.
func TestProposeAcceptsAValueAtTheMintFloor(t *testing.T) {
	value := strings.Repeat("é", secretmatch.MinManagedSecretRunes)
	got := Propose(value, 0, secretmatch.MinManagedSecretRunes, true)
	if !got.Eligible {
		t.Fatalf("reason = %q, want the floor-length value accepted", got.Reason)
	}
	shorter := Propose(value, 0, secretmatch.MinManagedSecretRunes-1, true)
	if shorter.Eligible || shorter.Reason != IneligibleTooShort {
		t.Fatalf("reason = %q eligible = %v, want runes counted, not bytes", shorter.Reason, shorter.Eligible)
	}
}

func TestProposeRefusesAWhitespaceOnlySelection(t *testing.T) {
	got := Propose(`token: "   "`, 7, 12, true)
	if got.Eligible || got.Reason != IneligibleEmpty {
		t.Fatalf("reason = %q, want empty", got.Reason)
	}
}

func TestProposeRefusesARangeTheDocumentDoesNotContain(t *testing.T) {
	got := Propose("short", 0, 400, true)
	if got.Eligible || got.Reason != IneligibleOutOfRange {
		t.Fatalf("reason = %q, want out_of_range", got.Reason)
	}
}

func TestProposeCountsRunesNotBytes(t *testing.T) {
	text := "key = \"café-au-lait-token-value\""
	got := Propose(text, 6, len([]rune(text)), true)
	if !got.Eligible {
		t.Fatalf("expected eligible, got %q", got.Reason)
	}
	if value := string([]rune(text)[got.Start:got.End]); value != "café-au-lait-token-value" {
		t.Fatalf("captured %q across a multi-byte rune", value)
	}
	if got.RuneLength == got.ByteLength {
		t.Fatal("rune and byte lengths must differ for multi-byte content")
	}
}

func TestSliceRefusesARangePastTheDocument(t *testing.T) {
	if _, ok := Slice("abc", 0, 9); ok {
		t.Fatal("slice past the end must not succeed")
	}
}

func TestSliceReturnsExactBytesAtRuneOffsets(t *testing.T) {
	got, ok := Slice("aé-token", 2, 8)
	if !ok || got != "-token" {
		t.Fatalf("slice = %q ok = %v, want \"-token\"", got, ok)
	}
}

// The screening minimum admits four-digit PINs.
func TestProposeAcceptsAFourDigitPIN(t *testing.T) {
	got := Propose("1234", 0, 4, true)
	if !got.Eligible {
		t.Fatalf("a four-digit PIN was refused: reason=%q", got.Reason)
	}
}
