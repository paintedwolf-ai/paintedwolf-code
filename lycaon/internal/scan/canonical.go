package scan

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/project"
)

// CanonicalPath returns EvalSymlinks+Clean identity for scan dedup and storage.
func CanonicalPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("canonical_path required")
	}
	return project.ResolveExistingDir(path)
}
