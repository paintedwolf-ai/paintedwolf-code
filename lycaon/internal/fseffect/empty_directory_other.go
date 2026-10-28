//go:build !darwin && !linux && !windows

package fseffect

func RemoveEmptyDirectoryGuarded(Location, string) error { return ErrUnsupported }
