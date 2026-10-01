package bloblifecycle

import (
	"fmt"
	"os"
	"path/filepath"
)

// RemoveTree releases managed files only after all archive readers finish.
// Database deletion commits before the lifecycle lock is acquired.
func RemoveTree(dataDir, path string) error {
	root := filepath.Clean(dataDir)
	rel, err := filepath.Rel(root, filepath.Clean(path))
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return fmt.Errorf("managed cleanup path is outside the data directory")
	}
	lifecycle := ForDevice(dataDir)
	lifecycle.Lock()
	defer lifecycle.Unlock()
	return os.RemoveAll(path)
}
