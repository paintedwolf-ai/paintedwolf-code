package ocrengine

import (
	"os"
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
