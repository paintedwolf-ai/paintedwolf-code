package modelstore

import (
	"errors"
	"os"
	"path/filepath"
)

// Persist every directory entry connecting published content to the filesystem root.
func syncPublicationDirectories(root *os.Root, name string, syncDirectory func(*os.Root, string) error) error {
	for {
		if err := syncDirectory(root, name); err != nil {
			return err
		}
		if name == "." {
			break
		}
		name = filepath.Dir(name)
	}
	path, err := filepath.Abs(root.Name())
	if err != nil {
		return err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	for parent := filepath.Dir(path); parent != path; parent = filepath.Dir(path) {
		directory, err := os.OpenRoot(parent)
		if err != nil {
			return err
		}
		err = errors.Join(syncDirectory(directory, "."), directory.Close())
		if err != nil {
			return err
		}
		path = parent
	}
	return nil
}
