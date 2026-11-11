//go:build !darwin && !linux

package testvolume

import "testing"

// New has no second filesystem to offer on this platform.
func New(t *testing.T, kind Kind) (*Volume, bool) {
	t.Skip("test volumes are unavailable on this platform")
	return nil, true
}

func sharedVolume(t *testing.T, kind Kind) (*Volume, bool) {
	t.Skip("test volumes are unavailable on this platform")
	return nil, true
}

func sweepStrays() {}
