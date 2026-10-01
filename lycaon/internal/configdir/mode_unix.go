//go:build !windows

package configdir

import (
	"errors"
	"os"
)

// restrictConfigDir tightens a root that already existed with a looser mode.
func restrictConfigDir(dir string) (err error) {
	root, err := os.Open(dir) // #nosec G703 -- UserConfigDir selects the configuration root.
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	info, err := root.Stat()
	if err != nil {
		return err
	}
	if info.Mode().Perm() == configDirMode {
		return nil
	}
	return root.Chmod(configDirMode)
}
