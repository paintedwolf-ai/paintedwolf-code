//go:build !darwin && !linux

package fseffect

// LinkIntoStaging is unavailable here; callers fall back to copying.
func LinkIntoStaging(_, _ Location) error { return ErrUnsupported }
