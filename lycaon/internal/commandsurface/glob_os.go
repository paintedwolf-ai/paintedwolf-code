package commandsurface

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// OSGlobFiles reads the host filesystem beneath Dir for the restricted
// structured command path. Readable is the launch boundary's read verdict.
type OSGlobFiles struct {
	Dir      string
	Readable func(abs string) bool
}

var _ GlobFiles = OSGlobFiles{}

func (f OSGlobFiles) ListDir(_ context.Context, dir string, maximum int) ([]GlobEntry, error) {
	abs := f.abs(dir)
	if !f.readable(abs) {
		return nil, nil
	}
	file, err := os.Open(abs)
	if err != nil {
		return nil, nil
	}
	defer func() { _ = file.Close() }()
	listed, _ := file.ReadDir(maximum + 1)
	entries := make([]GlobEntry, 0, len(listed))
	for _, entry := range listed {
		link := entry.Type()&os.ModeSymlink != 0
		isDir := entry.IsDir()
		if link {
			info, err := os.Stat(filepath.Join(abs, entry.Name()))
			isDir = err == nil && info.IsDir()
		}
		entries = append(entries, GlobEntry{Name: entry.Name(), Dir: isDir, Link: link})
	}
	return entries, nil
}

func (f OSGlobFiles) Entry(_ context.Context, path string) (GlobEntry, bool, error) {
	abs := f.abs(path)
	if !f.readable(abs) {
		return GlobEntry{}, false, nil
	}
	info, err := os.Stat(abs)
	if err != nil {
		//nolint:nilerr // Unstatable glob candidates are absent, not readable authority.
		return GlobEntry{}, false, nil
	}
	return GlobEntry{Name: filepath.Base(abs), Dir: info.IsDir()}, true, nil
}

func (f OSGlobFiles) Admit(_ context.Context, path string) (bool, error) {
	return f.readable(f.abs(path)), nil
}

func (f OSGlobFiles) abs(rel string) string {
	if strings.HasPrefix(rel, "/") {
		return filepath.FromSlash(rel)
	}
	if rel == "" {
		return f.Dir
	}
	return filepath.Join(f.Dir, filepath.FromSlash(rel))
}

func (f OSGlobFiles) readable(abs string) bool {
	return f.Readable == nil || f.Readable(abs)
}
