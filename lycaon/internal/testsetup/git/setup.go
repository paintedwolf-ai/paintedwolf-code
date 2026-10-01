// Package gittestsetup enables the repository's pinned Git for test binaries.
package gittestsetup

import "github.com/lycaon/lycaon/internal/gitengine"

// Enable configures the staged Git binary when present.
func Enable() {
	gitengine.TestingEnableBundledBinary()
}
