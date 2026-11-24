package progress

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// AuthoringToolID is the sole coordinator tool that mutates session progress rows.
const AuthoringToolID = "update_progress"

var shadowBoardRelPaths = []string{
	settingsoverlay.Rel("plans/board.md"),
	settingsoverlay.Rel("plans/board.plan.md"),
	settingsoverlay.Rel("scratchpad/board.md"),
	settingsoverlay.Rel("workbook/workbook.md"),
}

// IsShadowBoardPath reports whether rel is a reserved file-board path.
func IsShadowBoardPath(rel string) bool {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	for _, shadow := range shadowBoardRelPaths {
		if rel == shadow {
			return true
		}
	}
	return false
}
