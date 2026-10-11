package promotionstate

import (
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

// Promote candidates exclude engine state and VCS metadata, not ignored project files.
func TestFilterOverlayPromoteCandidatePaths_engineAndSkipSet(t *testing.T) {
	in := []string{
		"src/main.py",
		".env", // .gitignore-covered, but editable — kept
		"src/__pycache__/main.cpython-314.pyc",
		tooloutput.PromoteSpillDir + "/job-a.json",
		settingsoverlay.Rel("overlays/job-a/builtins.py"),
	}
	got := FilterOverlayPromoteCandidatePaths(in)
	want := []string{"src/main.py", ".env", "src/__pycache__/main.cpython-314.pyc"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("FilterOverlayPromoteCandidatePaths = %v want %v", got, want)
	}
}
