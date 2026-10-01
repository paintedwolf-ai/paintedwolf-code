package gitengine

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/configdir"
)

var hooksMu sync.Mutex

// EmptyHooksDir returns a verified empty hooks directory.
func EmptyHooksDir() (string, error) {
	hooksMu.Lock()
	defer hooksMu.Unlock()

	root, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "gitengine", "empty-hooks")

	if err := assertHooksDirEmpty(dir); err == nil {
		return filepath.Abs(dir)
	} else if !os.IsNotExist(err) {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o500); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // G302 — intentional 0500
		return "", err
	}
	if err := assertHooksDirEmpty(dir); err != nil {
		return "", err
	}
	return filepath.Abs(dir)
}

func assertHooksDirEmpty(dir string) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(ents) > 0 {
		names := make([]string, 0, len(ents))
		for _, e := range ents {
			names = append(names, e.Name())
		}
		return fmt.Errorf("gitengine: empty-hooks dir is not empty: %v", names)
	}
	return nil
}
