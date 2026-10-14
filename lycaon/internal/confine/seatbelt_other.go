//go:build !darwin && !linux

package confine

import "fmt"

// Available reports whether per-command confinement is enforced.
func Available() bool { return false }

func browserBoundarySupported() bool { return false }

func renderProfile(Confinement) (string, error) {
	return "", fmt.Errorf("per-command sandbox not implemented on this platform")
}

// applyAndExec fails on unsupported platforms.
func applyAndExec([]byte, string, []string) error {
	return fmt.Errorf("per-command sandbox not implemented on this platform")
}
