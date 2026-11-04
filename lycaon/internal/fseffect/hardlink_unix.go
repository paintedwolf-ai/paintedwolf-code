//go:build darwin || linux

package fseffect

import (
	"fmt"
	"io/fs"

	"golang.org/x/sys/unix"
)

// LinkIntoStaging gives a regular file a second name through held parents. It never replaces an
// entry or follows a symlink. Unlike HardLink it flushes nothing: its caller flushes the staged
// tree before publishing it, so linking many files costs one flush per directory.
func LinkIntoStaging(from, to Location) error {
	source, err := openParent(from, false, 0)
	if err != nil {
		return err
	}
	defer source.close()
	destination, err := openParent(to, true, 0o700)
	if err != nil {
		return err
	}
	defer destination.close()
	var st unix.Stat_t
	if err := unix.Fstatat(source.fd, source.name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &fs.PathError{Op: "stat link source", Path: source.name, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("%w: hard link source %s is not a regular file", ErrPostcondition, source.name)
	}
	if err := unix.Linkat(source.fd, source.name, destination.fd, destination.name, 0); err != nil {
		return &fs.PathError{Op: "hard link", Path: destination.name, Err: err}
	}
	return nil
}
