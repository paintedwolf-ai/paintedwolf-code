package worker_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/worker"
)

func TestBranchDeltaShowsPrimaryToBranchDiff(t *testing.T) {
	delta := worker.BranchDelta("line1\nline2\n", "line1\nline2 changed\n")
	if !strings.Contains(delta, "- line2") {
		t.Fatalf("delta=%q", delta)
	}
	if !strings.Contains(delta, "+ line2 changed") {
		t.Fatalf("delta=%q", delta)
	}
}

func TestBranchDeltaEmptyWhenIdentical(t *testing.T) {
	if got := worker.BranchDelta("same\n", "same\n"); got != "" {
		t.Fatalf("delta=%q want empty", got)
	}
}
