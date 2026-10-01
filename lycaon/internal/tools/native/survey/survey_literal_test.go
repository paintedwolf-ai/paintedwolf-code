package survey

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLiteralScanCountsEveryTermWithoutTypeBias(t *testing.T) {
	automaton := newLiteralAutomaton([]string{"kNoCellIdx", "uint32_t"})
	var matches []grepMatch
	err := scanLiteralLines(context.Background(), strings.NewReader(
		"const uint32_t kNoCellIdx = 0;\nuint32_t other = kNoCellIdx;\n",
	), "cell.h", automaton, func(match grepMatch) {
		matches = append(matches, match)
	})
	testutil.FailErr(t, "scan literals", err)
	counts := map[string]int{}
	for _, match := range matches {
		counts[match.Match] = match.Count
	}
	if counts["kNoCellIdx"] != 2 || counts["uint32_t"] != 2 {
		t.Fatalf("counts = %v", counts)
	}
}

func TestLiteralScanRejectsBinaryAfterAnEarlyMatch(t *testing.T) {
	automaton := newLiteralAutomaton([]string{"needle"})
	called := false
	err := scanLiteralLines(context.Background(), strings.NewReader("needle\n\x00binary"), "blob", automaton, func(grepMatch) {
		called = true
	})
	testutil.FailErr(t, "scan binary", err)
	if called {
		t.Fatal("binary content produced a literal match")
	}
}

func TestLiteralScanAcceptsTextControls(t *testing.T) {
	automaton := newLiteralAutomaton([]string{"needle"})
	called := false
	err := scanLiteralLines(context.Background(), strings.NewReader("\x1bneedle\f"), "source.txt", automaton, func(grepMatch) {
		called = true
	})
	testutil.FailErr(t, "scan control text", err)
	if !called {
		t.Fatal("control text did not produce a literal match")
	}
}
