//go:build !linux && !windows

package desktoptrash

func removeTrashMetadata(Receipt) error { return nil }
