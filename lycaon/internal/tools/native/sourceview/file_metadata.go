package sourceview

import (
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/sandbox"
)

// SymlinkTarget returns an in-project target path.
func SymlinkTarget(projectDir, relPath, target string) string {
	if filepath.IsAbs(target) {
		root, err := filepath.Abs(projectDir)
		if err != nil {
			return ""
		}
		rel, err := filepath.Rel(root, target)
		if err != nil || sandbox.HasParentTraversal(rel) {
			return ""
		}
		return filepath.ToSlash(rel)
	}
	joined := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(relPath), target)))
	if filepath.IsAbs(joined) || sandbox.HasParentTraversal(joined) {
		return ""
	}
	return joined
}

// FormatFileMode renders the approval bits of a file mode as octal (e.g. "644").
func FormatFileMode(infoMode uint32) string {
	return fmt.Sprintf("%03o", infoMode&0o777)
}
