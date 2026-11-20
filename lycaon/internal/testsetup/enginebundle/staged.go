// Package enginebundle locates resources staged for tests in the current checkout.
package enginebundle

import (
	"os"
	"path/filepath"
)

// Root returns the checkout's staging directory.
func Root() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "Taskfile.yml")); err == nil {
			return filepath.Join(dir, "lycaon-den", "src-tauri", "engine-root")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
