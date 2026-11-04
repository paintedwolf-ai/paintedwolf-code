package localdata

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// retiredWorkerSeedsDirName held the bridge copies an earlier engine kept for workers. Nothing writes
// it any more, and everything in it was rebuildable from the project.
const retiredWorkerSeedsDirName = "worker-seeds"

// ClearRetiredWorkerSeeds removes that directory and nothing else under the config root.
func ClearRetiredWorkerSeeds(base string) error {
	if strings.TrimSpace(base) == "" {
		return nil
	}
	dir := filepath.Join(base, retiredWorkerSeedsDirName)
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	return os.RemoveAll(dir)
}
