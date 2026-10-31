package projectpaths

import (
	"errors"
	"io/fs"
	"syscall"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// TestPath observes metadata or effective-identity access under this path's
// retained root and current read scope. A writable answer grants no write.
func (p BoundPath) TestPath(operator string) (bool, error) {
	var value bool
	var err error
	switch operator {
	case "-r", "-w", "-x":
		mode := fseffect.ReadAccess
		if operator == "-w" {
			mode = fseffect.WriteAccess
		}
		if operator == "-x" {
			mode = fseffect.ExecuteAccess
		}
		root, openErr := p.openEntryRoot()
		if openErr != nil {
			err = openErr
			break
		}
		defer func() { _ = root.Close() }()
		value, err = root.Access(p.exactRelative, mode, p.admitRead)
	case "-e", "-f", "-d", "-L", "-s":
		var stat fseffect.EntryStat
		stat, err = p.Stat(operator != "-L")
		if err != nil {
			break
		}
		switch operator {
		case "-e":
			value = true
		case "-f":
			value = stat.Mode.IsRegular()
		case "-d":
			value = stat.Mode.IsDir()
		case "-L":
			value = stat.Mode&fs.ModeSymlink != 0
		case "-s":
			value = stat.Size > 0
		}
	default:
		return false, fseffect.ErrInvalidPath
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return false, nil
	}
	return value, PathOperationError(p.Resolved.DisplayPath, err)
}
