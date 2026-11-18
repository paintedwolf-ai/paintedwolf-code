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

type installation struct {
	binary string
	lfs    string
}

var installed = newResolver()

func newResolver() func() (installation, error) {
	return sync.OnceValues(resolve)
}

// BinaryPath returns the bundled binary after one version check.
func BinaryPath() (string, error) {
	path, err := installed()
	return path.binary, err
}

// LFSPath returns the bundled Git LFS binary.
func LFSPath() (string, error) {
	path, err := installed()
	return path.lfs, err
}
