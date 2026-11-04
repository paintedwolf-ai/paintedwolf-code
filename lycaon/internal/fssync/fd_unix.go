//go:build darwin || linux

package fssync

// FD flushes a held descriptor to stable storage, by the same path as File.
func FD(fd int) error {
	if relaxed.Load() {
		return nil
	}
	return flushFD(fd)
}
