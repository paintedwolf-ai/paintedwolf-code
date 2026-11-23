package modelstore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The journal lock excludes materialization while collecting unpublished trees.
func collectRevisionTrees(root *os.Root, keep map[string]bool) error {
	return fs.WalkDir(root.FS(), "revisions", func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) && path == "revisions" {
			return nil
		}
		if err != nil {
			return err
		}
		if strings.Count(path, "/") != 3 || !entry.IsDir() {
			return nil
		}
		name := filepath.FromSlash(path)
		if !keep[name] {
			if err := root.RemoveAll(name); err != nil {
				return err
			}
		}
		return fs.SkipDir
	})
}

// Only partials belonging to the requested download can resume.
func (s *Store) cleanInterrupted(m Manifest, resumable bool) (resultErr error) {
	if _, err := s.Collect(); err != nil {
		return err
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	if err := removeAbandonedStages(root); err != nil {
		return err
	}
	entries, err := fs.ReadDir(root.FS(), "partials")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	keep := make(map[string]bool)
	if resumable {
		for _, a := range manifestFiles(m) {
			keep[a.Digest] = true
		}
	}
	for _, e := range entries {
		if !keep[e.Name()] {
			if err := root.RemoveAll(filepath.Join("partials", e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func removeAbandonedStages(root *os.Root) error {
	return fs.WalkDir(root.FS(), "artifacts", func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) && path == "artifacts" {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".staging-") {
			return root.Remove(filepath.FromSlash(path))
		}
		return nil
	})
}
