package git

import (
	"errors"
	"fmt"
	"os"
)

// ErrCloneDestinationExists refuses every existing destination, including empty
// directories and dangling symlinks. Clone never adopts a pre-existing folder.
var ErrCloneDestinationExists = errors.New("clone destination already exists")

type cloneDestination struct {
	path string
	info os.FileInfo
}

// The destination lease covers the exclusive claim, clone, and rollback.
func claimCloneDestination(path string) (cloneDestination, error) {
	if err := os.Mkdir(path, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return cloneDestination{}, fmt.Errorf("%w: %s", ErrCloneDestinationExists, path)
		}
		return cloneDestination{}, fmt.Errorf("create clone destination: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return cloneDestination{}, fmt.Errorf("inspect clone destination: %w", err)
	}
	return cloneDestination{path: path, info: info}, nil
}

func (d cloneDestination) failed(cause error) error {
	info, err := os.Lstat(d.path)
	if errors.Is(err, os.ErrNotExist) {
		return cause
	}
	if err != nil {
		return errors.Join(cause, fmt.Errorf("inspect failed clone destination: %w", err))
	}
	if !os.SameFile(d.info, info) {
		return errors.Join(cause, fmt.Errorf("clone destination replaced; retained %s", d.path))
	}
	// Empty-directory removal preserves files added by concurrent writers.
	if err := os.Remove(d.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(cause, fmt.Errorf("failed clone retained at %s: %w", d.path, err))
	}
	return cause
}
