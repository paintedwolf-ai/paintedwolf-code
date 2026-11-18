package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/textguard"
)

// TestFetchHygieneContract locks the shared invisible-codepoint table:
// zero-width, BiDi, and Unicode Tags. The extract walk's comment and hidden
// element drops are covered by webresearch
// TestExtractHTMLTextDropsCommentsAndHiddenElements.
func TestFetchHygieneContract(t *testing.T) {
	t.Parallel()
	// The agent path shares textguard.InvisibleFormatRune.
	if !textguard.InvisibleFormatRune(0x200B) ||
		!textguard.InvisibleFormatRune(0xFEFF) ||
		!textguard.InvisibleFormatRune(0x202E) ||
		!textguard.InvisibleFormatRune(0x2066) ||
		!textguard.InvisibleFormatRune(0xE0001) ||
		!textguard.InvisibleFormatRune(0xE007F) {
		t.Fatal("InvisibleFormatRune must cover zero-width, BiDi, and Unicode Tags")
	}

	smuggled := "Visible" + string(rune(0xE0001)) + "INJECT" + string(rune(0xE007F)) +
		"\u200B\uFEFF\u202E\u2066 end"
	stripped := textguard.StripInvisibleFormatRunesUntilStable(smuggled)
	for _, r := range []rune{0xE0001, 0xE007F, 0x200B, 0xFEFF, 0x202E, 0x2066} {
		if strings.ContainsRune(stripped, r) {
			t.Fatalf("strip left U+%04X in %q", r, stripped)
		}
	}
	if !strings.Contains(stripped, "Visible") || !strings.Contains(stripped, "end") {
		t.Fatalf("visible text lost: %q", stripped)
	}
}
