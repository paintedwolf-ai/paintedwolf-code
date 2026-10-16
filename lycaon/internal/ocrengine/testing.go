package ocrengine

import (
	"os"
	"path/filepath"
	"strings"
)

// TestingEnableBundledRoot points a test binary at the checkout's staged engine
// when one exists. A test binary has no engine root beside it.
func TestingEnableBundledRoot() {
	if strings.TrimSpace(os.Getenv(EnvRoot)) != "" {
		return
	}
	root := findStagedRoot()
	if root == "" {
		return
	}
	_ = os.Setenv(EnvRoot, root)
	resetForTest()
}

func findStagedRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, "lycaon-den", "src-tauri", "engine-root", DirName)
		if st, err := os.Stat(filepath.Join(candidate, filepath.FromSlash(binaryRel))); err == nil && !st.IsDir() {
			return candidate
		}
		if _, err := os.Stat(filepath.Join(dir, "Taskfile.yml")); err == nil {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
