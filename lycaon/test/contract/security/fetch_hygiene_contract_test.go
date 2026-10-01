package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/textguard"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestFetchHygieneContract locks the content half of fetch hygiene: the extract
// walk drops what a reader cannot see, and one shared invisible-codepoint table
// covers zero-width, BiDi, and Unicode Tags. Retrieval marking derives from
// message provenance; TestToolHandlersDoNotMarkTheirOwnOutput covers it.
func TestFetchHygieneContract(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)

	fetchPath := filepath.Join(root, "lycaon", "internal", "webresearch", "fetch.go")
	fetchSrc, err := os.ReadFile(fetchPath)
	contractcheck.FailErr(t, "read fetch.go", err)
	if !strings.Contains(string(fetchSrc), "html.CommentNode") {
		t.Fatal("fetch.go must skip HTML comments in the extract walk")
	}
	if !strings.Contains(string(fetchSrc), "isHiddenElement") {
		t.Fatal("fetch.go must drop hidden-element subtrees in the extract walk")
	}

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
