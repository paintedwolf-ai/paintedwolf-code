package projectroot

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
)

// OutsideDirectoryIdentity identifies a canonical host directory from a held descriptor.
func OutsideDirectoryIdentity(abs string) (string, error) {
	root, err := fseffect.OpenMetadataRoot(OutsidePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	rel := strings.TrimPrefix(filepath.Clean(abs), OutsidePath)
	if rel == "" {
		rel = "."
	}
	file, err := root.OpenExact(rel, true)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	if file.Name() != abs {
		return "", fseffect.ErrSymlink
	}
	return fspath.FileIdentity(file)
}
