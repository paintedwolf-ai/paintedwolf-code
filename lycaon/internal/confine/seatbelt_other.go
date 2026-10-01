//go:build !darwin

package confine

import "fmt"

// Available reports whether per-command confinement is enforced.
func Available() bool { return false }

func applySeatbelt(string) error {
	return fmt.Errorf("per-command sandbox not implemented on this platform")
}

// attestConfined fails on unsupported platforms.
func attestConfined() error {
	return fmt.Errorf("per-command sandbox not implemented on this platform")
}
