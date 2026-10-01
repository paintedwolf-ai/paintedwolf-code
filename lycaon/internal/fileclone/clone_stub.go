//go:build !darwin && !linux

package fileclone

import "os"

// Clone attempts a filesystem copy-on-write clone, reporting unsupported filesystems without error.
func Clone(_, _ string) (bool, error) {
	return false, nil
}

func CloneInto(*os.File, *os.Root, string) (bool, error) { return false, nil }
