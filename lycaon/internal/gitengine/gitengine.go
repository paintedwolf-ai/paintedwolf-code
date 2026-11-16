// Package gitengine resolves the bundled pinned toolchain.
package gitengine

import (
	"fmt"
	"sync"
)

// EnvGitBinary overrides the bundled path in tests.
const EnvGitBinary = "LYCAON_GIT_BINARY"

// EnvTest enables test overrides.
const EnvTest = "LYCAON_TEST"

// UnavailableError reports a missing or mismatched toolchain.
type UnavailableError struct {
	Reason   string // "missing" | "version_mismatch"
	Expected string
	Found    string
}

func (e *UnavailableError) Error() string {
	switch e.Reason {
	case "version_mismatch":
		return fmt.Sprintf("gitengine: version mismatch: expected %s, found %s", e.Expected, e.Found)
	default:
		return "gitengine: bundled git unavailable"
	}
}

var (
	resolveOnce sync.Once
	cachedBin   string
	cachedLFS   string
	cachedErr   error
)

// BinaryPath returns the bundled binary after one version check.
func BinaryPath() (string, error) {
	resolveOnce.Do(resolveAll)
	return cachedBin, cachedErr
}

// LFSPath returns the bundled Git LFS binary.
func LFSPath() (string, error) {
	resolveOnce.Do(resolveAll)
	if cachedErr != nil {
		return "", cachedErr
	}
	return cachedLFS, nil
}

func resolveAll() {
	cachedBin, cachedLFS, cachedErr = resolve()
}
