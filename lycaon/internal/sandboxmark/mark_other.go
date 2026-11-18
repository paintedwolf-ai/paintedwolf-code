//go:build !darwin

package sandboxmark

import "github.com/lycaon/lycaon/internal/osprocess"

// Enable creates nothing: only macOS needs a sandbox to find a command's
// detached descendants.
func Enable() (string, error) { return "", nil }

// Processes finds nothing without a mark.
func Processes(string) []osprocess.Incarnation { return nil }
