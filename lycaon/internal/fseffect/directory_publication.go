package fseffect

import (
	"errors"
	"os"

	"github.com/lycaon/lycaon/internal/fssync"
)

// PublishDirectory restores the recorded mode through the same held directory after rename.
func PublishDirectory(root, from, to, identity string, mode os.FileMode) (result error) {
	dir, err := openGuardedDirectory(Location{Root: root, Rel: from}, identity)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, dir.Chmod(mode), fssync.File(dir), dir.Close()) }()
	if err := dir.Chmod(mode | 0o700); err != nil {
		return err
	}
	return RenameGuarded(root, from, to, identity)
}

// RestoreDirectoryMode completes a journaled directory publication after interruption.
func RestoreDirectoryMode(loc Location, identity string, mode os.FileMode) error {
	dir, err := openGuardedDirectory(loc, identity)
	if err != nil {
		return err
	}
	return errors.Join(dir.Chmod(mode), fssync.File(dir), dir.Close())
}
