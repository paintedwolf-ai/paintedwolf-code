package blueprintfile

import (
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"path/filepath"
	"strings"
)

// BoundBlueprintRelPath returns the bound convention path, or empty when unbound.
func BoundBlueprintRelPath(blueprintPath string) string {
	bound := strings.TrimSpace(blueprintPath)
	if strings.HasPrefix(bound, settingsoverlay.DirName()+"/blueprints/") && strings.HasSuffix(bound, ".md") {
		return filepath.ToSlash(bound)
	}
	return ""
}
