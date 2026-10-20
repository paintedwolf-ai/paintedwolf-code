//go:build darwin || linux

package fseffect

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// TestPath queries metadata/access beneath the held read root. Access predicates
// use the effective identity and OS ACL policy; they acquire no future operation
// authority and never open/create/truncate a target for writing.
func (r *ReadRoot) TestPath(rel, operator string) (bool, error) {
	clean, err := cleanReadLocation(Location{Root: r.root.path, Rel: rel})
	if err != nil {
		return false, err
	}
	result := false
	_, err = walkReadContained(r.root, clean.Rel, operator != "-L", func(dir *os.File, name string, st *unix.Stat_t) (*os.File, error) {
		mode := st.Mode & unix.S_IFMT
		switch operator {
		case "-e":
			result = true
		case "-f":
			result = mode == unix.S_IFREG
		case "-d":
			result = mode == unix.S_IFDIR
		case "-L":
			result = mode == unix.S_IFLNK
		case "-r", "-w", "-x":
			access := uint32(unix.R_OK)
			if operator == "-w" {
				access = unix.W_OK
			}
			if operator == "-x" {
				access = unix.X_OK
			}
			// NOFOLLOW prevents a final-entry swap from turning this query into an
			// out-of-root probe. Recheck identity after the platform's access decision.
			err := unix.Faccessat(int(dir.Fd()), name, access, unix.AT_EACCESS|unix.AT_SYMLINK_NOFOLLOW)
			if err != nil && !errors.Is(err, unix.EACCES) && !errors.Is(err, unix.EPERM) {
				return nil, err
			}
			var after unix.Stat_t
			if check := unix.Fstatat(int(dir.Fd()), name, &after, unix.AT_SYMLINK_NOFOLLOW); check != nil {
				return nil, check
			}
			if st.Dev != after.Dev || st.Ino != after.Ino || st.Mode != after.Mode {
				return nil, ErrPredicateChanged
			}
			result = err == nil
		default:
			return nil, ErrInvalidPath
		}
		return nil, nil
	})
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ENOTDIR) {
		return false, nil
	}
	return result, err
}
