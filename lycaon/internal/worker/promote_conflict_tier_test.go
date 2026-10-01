package worker

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestClassifyConflictTierLineShiftAfterSiblingDrift(t *testing.T) {
	base := strings.Repeat("line\n", 200)
	primary := base + "sibling insert\n"
	branch := base + "overlay insert\n"
	_, hunks, clean := ThreeWayMerge(base, primary, branch)
	if clean {
		t.Fatal("expected conflict")
	}
	for i := 0; i < 20; i++ {
		hunks = append(hunks, api.WorkerMergeHunk{StartLine: i * 5, EndLine: i*5 + 2, Primary: "p", Branch: "b"})
	}
	tier := ClassifyConflictTier(PromoteConflict{
		Base: base, Primary: primary, Branch: branch, Hunks: hunks,
	})
	if tier != api.WorkerPromoteConflictTierLineShift {
		t.Fatalf("tier = %q want line_shift", tier)
	}
}

func TestClassifyConflictTierOverlappingEditFewHunks(t *testing.T) {
	base := "a\nb\nc\n"
	primary := "a\nB\nC\n"
	branch := "a\nb\nC changed\n"
	_, hunks, clean := ThreeWayMerge(base, primary, branch)
	if clean {
		t.Fatal("expected conflict")
	}
	if len(hunks) > tooloutput.PromoteHighConflictHunkThreshold {
		t.Fatalf("hunks = %d", len(hunks))
	}
	tier := ClassifyConflictTier(PromoteConflict{
		Base: base, Primary: primary, Branch: branch, Hunks: hunks,
	})
	if tier != api.WorkerPromoteConflictTierOverlappingEdit {
		t.Fatalf("tier = %q want overlapping_edit", tier)
	}
}

func TestDigestSummaryForConflictPrefersBranchDeltaOnLineShift(t *testing.T) {
	c := PromoteConflict{
		Primary: "a\nb\n",
		Branch:  "a\nb\n+ new line\n",
		Hunks:   make([]api.WorkerMergeHunk, 15),
	}
	summary := digestSummaryForConflict(c, api.WorkerPromoteConflictTierLineShift)
	if len(summary) == 0 || !strings.Contains(summary[0], "+") {
		t.Fatalf("summary = %v", summary)
	}
}
