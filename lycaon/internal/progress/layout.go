package progress

import (
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"path/filepath"
	"strings"
)

// AuthoringToolID is the sole coordinator tool that writes the projected progress strip.
const AuthoringToolID = "update_progress"

// BootstrapPlaceholderGoal is written before the coordinator authors a run-scoped plan.
const BootstrapPlaceholderGoal = "Session goal pending."

// Shadow board paths the investigate surface must not use as a progress substitute.
var shadowBoardRelPaths = []string{
	settingsoverlay.Rel("plans/board.md"),
	settingsoverlay.Rel("plans/board.plan.md"),
	settingsoverlay.Rel("scratchpad/board.md"),
	settingsoverlay.Rel("workbook/workbook.md"),
}

// IsShadowBoardPath reports whether rel is a reserved file-board path that must not
// satisfy plan-authoring instructions — the strip reads session_progress only.
func IsShadowBoardPath(rel string) bool {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	for _, shadow := range shadowBoardRelPaths {
		if rel == shadow {
			return true
		}
	}
	return false
}
