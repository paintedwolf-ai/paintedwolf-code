//go:build !darwin && !linux

package confine

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/serviceguest"
)

// Available reports whether per-command confinement is enforced.
func Available() bool { return false }

func renderProfile(Confinement) (string, error) {
	return "", fmt.Errorf("per-command sandbox not implemented on this platform")
}

// applyAndExec fails on unsupported platforms.
func applyAndExec([]byte, string, []string) error {
	return fmt.Errorf("per-command sandbox not implemented on this platform")
}

// OpenPrivateGuest reports an unavailable private service runtime.
func OpenPrivateGuest(context.Context) (*serviceguest.Client, error) {
	return nil, serviceguest.ErrUnavailable
}
