package worker_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/worker"
)

func TestThreeWayMergePrimaryUnchanged(t *testing.T) {
	base := "a\nb\nc\n"
	ours := base
	theirs := "a\nB\nc\n"
	merged, hunks, clean := worker.ThreeWayMerge(base, ours, theirs)
	if !clean {
		t.Fatalf("expected clean merge hunks=%v", hunks)
	}
	if !strings.Contains(merged, "B") {
		t.Fatalf("merged=%q", merged)
	}
}

func TestThreeWayMergeConflictHunks(t *testing.T) {
	base := "a\nb\nc\n"
	ours := "a\nPRIMARY\nc\n"
	theirs := "a\nBRANCH\nc\n"
	_, hunks, clean := worker.ThreeWayMerge(base, ours, theirs)
	if clean || len(hunks) != 1 {
		t.Fatalf("clean=%v hunks=%v", clean, hunks)
	}
	if hunks[0].Primary != "PRIMARY" || hunks[0].Branch != "BRANCH" {
		t.Fatalf("hunk=%+v", hunks[0])
	}
}
