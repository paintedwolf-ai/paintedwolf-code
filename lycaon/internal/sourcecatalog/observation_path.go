package sourcecatalog

import (
	"context"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/repochange"
)

// Absolute internal links resolve to a confined, root-relative spelling.
func observationPath(root *os.Root, rel string) (string, error) {
	base := fspath.CanonicalPath(root.Name())
	target := fspath.CanonicalPath(filepath.Join(root.Name(), filepath.FromSlash(rel)))
	if base == "" || target == "" || repochange.IsPrivatePath(target) {
		return "", os.ErrPermission
	}
	resolved, err := filepath.Rel(filepath.FromSlash(base), filepath.FromSlash(target))
	if err != nil || !filepath.IsLocal(resolved) {
		return "", os.ErrPermission
	}
	resolved = filepath.ToSlash(resolved)
	return resolved, nil
}

type observationDirectory struct {
	handle        *os.File
	entries       *directoryKindReader
	resolved      string
	privateFilter *repochange.PrivateDirectoryFilter
	// stamp is taken before the first entry is read, so any change during the
	// listing leaves the recorded stamp behind.
	stamp DirectoryStamp
}

func (d *observationDirectory) close() {
	if d.entries != nil && d.entries.file != d.handle {
		_ = d.entries.file.Close()
	}
	if d.handle != nil {
		_ = d.handle.Close()
	}
}

func (c *Catalog) openObservationDirectory(ctx context.Context, store *indexStore, root *os.Root, dir string) (*observationDirectory, error) {
	release, err := c.broker.Acquire(ctx, store.observationRequest(dir))
	if err != nil {
		return nil, err
	}
	defer release()
	return openConfinedDirectory(root, dir)
}

func openConfinedDirectory(root *os.Root, dir string) (*observationDirectory, error) {
	resolved, err := observationPath(root, dir)
	if err != nil {
		return nil, err
	}
	file, err := openDirectoryFile(root, filepath.FromSlash(resolved))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	entries, err := directoryKinds(file)
	if err != nil {
		return nil, err
	}
	return &observationDirectory{
		entries:       entries,
		resolved:      resolved,
		privateFilter: repochange.NewPrivateDirectoryFilter(filepath.Join(root.Name(), filepath.FromSlash(resolved))),
		stamp:         directoryStampOf(info),
	}, nil
}
