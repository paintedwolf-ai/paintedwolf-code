package toolkit

import "testing"

func TestCountLinesTrailingNewline(t *testing.T) {
	if got := CountLines("a\nb\n"); got != 2 {
		t.Fatalf("CountLines = %d want 2", got)
	}
	if got := len(SplitLines("a\nb\n")); got != 2 {
		t.Fatalf("SplitLines len = %d want 2", got)
	}
}
