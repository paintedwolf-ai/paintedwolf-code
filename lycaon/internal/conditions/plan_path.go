package conditions

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/blueprintfile"
)

// PlanPathForProject returns the conventional on-disk blueprint path for display.
func PlanPathForProject(projectDir, blueprintPath string) string {
	projectDir = strings.TrimSpace(projectDir)
	rel := blueprintfile.BoundBlueprintRelPath(blueprintPath)
	if projectDir == "" || rel == "" {
		return ""
	}
	return filepath.Join(projectDir, rel)
}
