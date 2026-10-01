package promptattach

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTurnPreviewBudgetLargeTextAndTurnCeilings(t *testing.T) {
	caps, err := LoadCaps()
	testutil.FailErr(t, "load caps", err)
	budget := NewTurnPreviewBudget(caps)
	large := int64(caps.Composer.AutoAttachPaste)
	if got, want := budget.claimText(large), caps.Prompt.MaxLargeTextPreview.Int(); got != want {
		t.Fatalf("large text claim = %d want %d", got, want)
	}
	claimed := caps.Prompt.MaxLargeTextPreview.Int()
	for claimed < caps.Prompt.MaxTurnPreview.Int() {
		claimed += budget.claimBody(int64(caps.Prompt.MaxBodyPreview))
	}
	if claimed != caps.Prompt.MaxTurnPreview.Int() {
		t.Fatalf("turn claims = %d want %d", claimed, caps.Prompt.MaxTurnPreview.Int())
	}
	if got := budget.claimBody(1); got != 0 {
		t.Fatalf("exhausted claim = %d want 0", got)
	}
}

func TestTurnPreviewBudgetKeepsSmallTextWhole(t *testing.T) {
	caps, err := LoadCaps()
	testutil.FailErr(t, "load caps", err)
	budget := NewTurnPreviewBudget(caps)
	small := int64(caps.Composer.AutoAttachPaste) - 1
	if got := budget.claimText(small); int64(got) != small {
		t.Fatalf("small text claim = %d want %d", got, small)
	}
}

func TestExhaustedPreviewCarriesOnlyMetadata(t *testing.T) {
	fence := frameSnippet("result", "text/plain", "body must not leak", 0)
	if strings.Contains(fence, "body must not leak") {
		t.Fatal("exhausted preview leaked body text")
	}
	if !strings.Contains(fence, TruncationMarker(0, 18)) {
		t.Fatalf("fence missing exhausted marker: %s", fence)
	}
}

func TestTruncatePrefixDoesNotSplitUTF8(t *testing.T) {
	preview, kept, truncated := truncatePrefix("ab😀cd", 4)
	if preview != "ab" || kept != 2 || !truncated {
		t.Fatalf("bounded preview = %q, %d, %t", preview, kept, truncated)
	}
}
