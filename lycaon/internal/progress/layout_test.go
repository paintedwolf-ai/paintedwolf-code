package progress_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

func TestIsShadowBoardPath(t *testing.T) {
	for path, want := range map[string]bool{
		settingsoverlay.Rel("plans/board.md"):             true,
		settingsoverlay.Rel("plans/board.plan.md"):        true,
		settingsoverlay.Rel("scratchpad/board.md"):        true,
		settingsoverlay.Rel("workbook/workbook.md"):       true,
		" " + settingsoverlay.Rel("plans/board.md") + " ": true,
		settingsoverlay.Rel("plans/notes.plan.md"):        false,
		settingsoverlay.Rel("blueprints/board.md"):        false,
		"plans/board.md": false,
	} {
		if got := progress.IsShadowBoardPath(path); got != want {
			t.Errorf("IsShadowBoardPath(%q) = %v, want %v", path, got, want)
		}
	}
}
