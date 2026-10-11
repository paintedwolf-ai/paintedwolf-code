package projectsource

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/evidence"
)

// ResolveAbsentSourcePath checks one attached address without discovery.
// Existing nodes, including dangling symlinks, never become history opens.
func ResolveAbsentSourcePath(p ProjectSource, rootID, path string) (string, error) {
	rel, err := normalizeNavigableProjectPath(path)
	if err != nil {
		return "", err
	}
	if p == nil || rootID == "" {
		return "", ErrSourceNoRoot
	}
	for _, root := range p.SourceRoots() {
		if root.ID != rootID {
			continue
		}
		abs, canonicalPath, ok := evidence.ResolveCitationAbs(root.Path, rel)
		if !ok {
			return "", ErrSourcePathDenied
		}
		// Check the lexical leaf too: a dangling symlink is not a deletion.
		if _, err := os.Lstat(filepath.Join(root.Path, filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return "", err
			}
			return "", ErrSourceExists
		}
		if _, err := os.Lstat(abs); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return "", err
			}
			return "", ErrSourceExists
		}
		return canonicalPath, nil
	}
	return "", ErrSourceNotFound
}
