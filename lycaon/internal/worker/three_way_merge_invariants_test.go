package worker_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/worker"
)

// ThreeWayMerge invariants: identity, idempotency, line-ending normalization,
// and clean-merge behavior under degenerate inputs (empty files).

func TestThreeWayMergeAllEqualReturnsCleanInput(t *testing.T) {
	base, ours, theirs := "a\nb\n", "a\nb\n", "a\nb\n"
	merged, hunks, clean := worker.ThreeWayMerge(base, ours, theirs)
	if !clean || len(hunks) != 0 {
		t.Fatalf("expected clean no-op, clean=%v hunks=%v", clean, hunks)
	}
	if merged != ours {
		t.Fatalf("merged=%q want %q", merged, ours)
	}
}

func TestThreeWayMergeAllEmptyIsClean(t *testing.T) {
	merged, hunks, clean := worker.ThreeWayMerge("", "", "")
	if !clean || len(hunks) != 0 || merged != "" {
		t.Fatalf("expected clean empty, merged=%q hunks=%v clean=%v", merged, hunks, clean)
	}
}

func TestThreeWayMergeOnlyBranchChangedTakesBranch(t *testing.T) {
	base := "a\nb\n"
	ours := base
	theirs := "a\nX\n"
	merged, hunks, clean := worker.ThreeWayMerge(base, ours, theirs)
	if !clean || len(hunks) != 0 {
		t.Fatalf("expected clean, clean=%v hunks=%v", clean, hunks)
	}
	if merged != theirs {
		t.Fatalf("merged=%q want %q", merged, theirs)
	}
}

func TestThreeWayMergeOnlyPrimaryChangedTakesPrimary(t *testing.T) {
	base := "a\nb\n"
	ours := "a\nY\n"
	theirs := base
	merged, _, clean := worker.ThreeWayMerge(base, ours, theirs)
	if !clean || merged != ours {
		t.Fatalf("clean=%v merged=%q want %q", clean, merged, ours)
	}
}

func TestThreeWayMergeCRLFNormalizesToLFOnSlowPath(t *testing.T) {
	// All three inputs differ, so the line-by-line path runs and normalizes.
	base := "a\r\nb\r\nc\r\n"
	ours := "A\r\nb\r\nc\r\n"
	theirs := "a\r\nb\r\nC\r\n"
	merged, _, clean := worker.ThreeWayMerge(base, ours, theirs)
	if !clean {
		t.Fatalf("expected clean merge across CRLF inputs")
	}
	if strings.Contains(merged, "\r") {
		t.Fatalf("merged must not contain CR after normalization, got %q", merged)
	}
	if !strings.HasSuffix(merged, "\n") {
		t.Fatalf("merged must end with LF, got %q", merged)
	}
}

func TestThreeWayMergeFastPathReturnsBranchVerbatim(t *testing.T) {
	// When ours == base the branch wins byte for byte, without normalization.
	base := "a\r\nb\r\n"
	ours := base
	theirs := "x\r\ny\r\n"
	merged, hunks, clean := worker.ThreeWayMerge(base, ours, theirs)
	if !clean || len(hunks) != 0 {
		t.Fatalf("expected clean fast-path, hunks=%v", hunks)
	}
	if merged != theirs {
		t.Fatalf("fast path must return branch verbatim, got %q want %q", merged, theirs)
	}
}

func TestThreeWayMergeIdempotentOnReMerge(t *testing.T) {
	// A clean merge result is a fixed point of re-merging against itself.
	base := "a\nb\nc\n"
	ours := base
	theirs := "a\nB\nc\n"
	merged, _, clean := worker.ThreeWayMerge(base, ours, theirs)
	if !clean {
		t.Fatalf("seed merge not clean")
	}
	again, _, clean2 := worker.ThreeWayMerge(merged, merged, merged)
	if !clean2 || again != merged {
		t.Fatalf("re-merge not idempotent: first=%q second=%q clean=%v", merged, again, clean2)
	}
}

func TestThreeWayMergeConflictHunkLineRangeAlignsWithOutput(t *testing.T) {
	base := "a\nb\nc\n"
	ours := "a\nP\nc\n"
	theirs := "a\nB\nc\n"
	merged, hunks, clean := worker.ThreeWayMerge(base, ours, theirs)
	if clean || len(hunks) != 1 {
		t.Fatalf("expected one conflict hunk, clean=%v hunks=%v", clean, hunks)
	}
	h := hunks[0]
	if h.StartLine < 1 || h.EndLine < h.StartLine {
		t.Fatalf("bogus hunk range %+v", h)
	}
	// The hunk range lies within the merged output that carries the markers.
	lines := strings.Split(merged, "\n")
	if h.EndLine > len(lines) {
		t.Fatalf("hunk EndLine=%d exceeds merged line count=%d", h.EndLine, len(lines))
	}
	if !strings.Contains(merged, "<<<<<<< primary") || !strings.Contains(merged, ">>>>>>> branch") {
		t.Fatalf("merged must contain conflict markers, got %q", merged)
	}
}

func TestThreeWayMergeDualIdenticalChangeIsClean(t *testing.T) {
	base := "a\nb\nc\n"
	ours := "a\nX\nc\n"
	theirs := "a\nX\nc\n"
	merged, hunks, clean := worker.ThreeWayMerge(base, ours, theirs)
	if !clean || len(hunks) != 0 {
		t.Fatalf("expected clean merge for identical change, clean=%v hunks=%v", clean, hunks)
	}
	if merged != ours {
		t.Fatalf("merged=%q want %q", merged, ours)
	}
}

func TestThreeWayMergeMissingTrailingNewlineNormalizedOnSlowPath(t *testing.T) {
	// On the line-by-line merge path, joinMergeLines guarantees a trailing newline
	// even when none of the inputs ended in one.
	base := "a\nb"
	ours := "A\nb"
	theirs := "a\nB"
	merged, _, _ := worker.ThreeWayMerge(base, ours, theirs)
	if merged == "" {
		t.Fatalf("merged should not be empty for non-empty inputs")
	}
	if !strings.HasSuffix(merged, "\n") {
		t.Fatalf("merged must end with newline on slow path; got %q", merged)
	}
}
