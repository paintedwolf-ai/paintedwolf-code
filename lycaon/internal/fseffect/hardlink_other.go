//go:build !darwin && !linux

package fseffect

// HardLink is unavailable here; callers fall back to copying.
func HardLink(_, _ Location) error { return ErrUnsupported }
