//go:build darwin || linux

package fseffect

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Access queries effective-identity access through the held read walk. admit
// checks the resolved entry before its metadata or access decision is returned.
func (r *ReadRoot) Access(rel string, mode AccessMode, admit func(string) error) (bool, error) {
	access := uint32(unix.R_OK)
	switch mode {
	case ReadAccess:
	case WriteAccess:
		access = unix.W_OK
	case ExecuteAccess:
		access = unix.X_OK
	default:
		return false, ErrInvalidPath
	}
	relative, err := exactReadRelative(rel)
	if err != nil {
		return false, err
	}
	result := false
	_, err = walkReadContained(r.root, relative, true, func(dir *os.File, name string, st *unix.Stat_t) (*os.File, error) {
		if admit != nil {
			if err := admit(filepath.Join(dir.Name(), name)); err != nil {
				return nil, err
			}
		}
		if strings.HasSuffix(rel, "/") && st.Mode&unix.S_IFMT != unix.S_IFDIR {
			return nil, nil
		}
		decision := unix.Faccessat(int(dir.Fd()), name, access, unix.AT_EACCESS|unix.AT_SYMLINK_NOFOLLOW)
		if decision != nil && !errors.Is(decision, unix.EACCES) && !errors.Is(decision, unix.EPERM) {
			return nil, decision
		}
		var after unix.Stat_t
		if err := unix.Fstatat(int(dir.Fd()), name, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return nil, err
		}
		if st.Dev != after.Dev || st.Ino != after.Ino || st.Mode != after.Mode {
			return nil, ErrPredicateChanged
		}
		result = decision == nil
		return nil, nil
	})
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ENOTDIR) {
		return false, nil
	}
	return result, err
}
