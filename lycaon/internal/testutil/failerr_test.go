package testutil

import (
	"strings"
	"testing"
)

func TestFormatSetDiffShowsSymmetricDiff(t *testing.T) {
	msg := FormatSetDiff("scope", []string{"a", "b", "c"}, []string{"a", "b", "d"})
	if !strings.Contains(msg, "only in expected: c") {
		t.Fatalf("missing only-in-expected:\n%s", msg)
	}
	if !strings.Contains(msg, "only in actual:   d") {
		t.Fatalf("missing only-in-actual:\n%s", msg)
	}
}

func TestFormatSetDiffDuplicateCount(t *testing.T) {
	msg := FormatSetDiff("dupes", []string{"a", "a"}, []string{"a"})
	if !strings.Contains(msg, "duplicate count differs") {
		t.Fatalf("expected duplicate hint:\n%s", msg)
	}
}
