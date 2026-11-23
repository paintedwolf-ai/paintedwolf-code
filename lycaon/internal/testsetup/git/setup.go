// Package gittestsetup enables the repository's pinned Git for test binaries.
package gittestsetup

import (
	"os"

	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/testsetup/enginebundle"
)

// Enable configures staged Git before the test process resolves it.
func Enable() {
	if bin := gitengine.BundledBinary(enginebundle.Root()); bin != "" {
		_ = os.Setenv(gitengine.EnvTest, "1")
		_ = os.Setenv(gitengine.EnvGitBinary, bin)
	}
}
