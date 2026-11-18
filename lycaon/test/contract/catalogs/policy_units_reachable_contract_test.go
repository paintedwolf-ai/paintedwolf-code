package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
)

// hintViewShipsCopy reports whether the projected hint view carries copy beyond
// the bare-code fallback.
func hintViewShipsCopy(id string, entry guidance.HintEntry) bool {
	view := guidance.NormalizeRejectCodeView(id, entry)
	return view.What != id || view.Cause != "" || view.Why != "" || view.Fix != "" || view.Instead != ""
}

func TestHintViewShipsCopy(t *testing.T) {
	t.Parallel()
	if hintViewShipsCopy("X", guidance.HintEntry{}) {
		t.Error("empty entry must not count as copy")
	}
	if !hintViewShipsCopy("X", guidance.HintEntry{What: "real copy"}) {
		t.Error("what copy must count")
	}
	if !hintViewShipsCopy("X", guidance.HintEntry{Fix: "do this"}) {
		t.Error("fix copy must count")
	}
}
