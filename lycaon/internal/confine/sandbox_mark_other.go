//go:build !darwin

package confine

import "github.com/lycaon/lycaon/internal/osprocess"

// EnableSandboxMark creates nothing: only macOS needs a sandbox to find a
// command's detached descendants.
func EnableSandboxMark() (string, error) { return "", nil }

// MarkedProcesses finds nothing without a sandbox mark.
func MarkedProcesses(string) []osprocess.Incarnation { return nil }
