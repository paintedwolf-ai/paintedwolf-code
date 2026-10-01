package workercompletion

import (
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
)

// dirHasWorkspaceFiles reports whether a non-git project root holds any work.
// It declines only what every project walk declines, VCS and engine-overlay
// metadata, so a root holding just `.github/` still counts.
func dirHasWorkspaceFiles(projectDir string) (bool, error) {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return false, nil
	}
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, entry := range entries {
		if sandbox.ShouldSkipDirBaseName(entry.Name()) {
			continue
		}
		return true, nil
	}
	return false, nil
}
