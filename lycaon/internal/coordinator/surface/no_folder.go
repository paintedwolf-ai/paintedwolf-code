package surface

import (
	"github.com/lycaon/lycaon/internal/toolscope"
)

// FilterSurfaceToolsForRootCount restricts tools when no project root is open.
func FilterSurfaceToolsForRootCount(tools []string, rootCount int) ([]string, error) {
	if rootCount != 0 {
		return append([]string(nil), tools...), nil
	}
	// An unreadable catalog closes the surface.
	if _, err := toolscope.NoFolderAllowlist(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(tools))
	for _, name := range tools {
		if toolscope.AllowedWithNoFolder(name) {
			out = append(out, name)
		}
	}
	return out, nil
}
