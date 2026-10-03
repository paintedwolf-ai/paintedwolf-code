package sourcesnapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Materialize creates an isolated tree of verified snapshot bytes. It never
// substitutes a newer live body when the recorded content is unavailable.
// The caller owns the returned tree and removes it after the consumer stops.
func (s *Store) Materialize(ctx context.Context, id, root string, paths []string) (string, error) {
	release, err := s.acquireStorage(ctx, false)
	if err != nil {
		return "", err
	}
	defer release()
	snapshot, err := s.Get(ctx, id)
	if err != nil {
		return "", err
	}
	releaseRoots, err := s.acquireRootStorage(ctx, snapshot.Roots, false)
	if err != nil {
		return "", err
	}
	defer releaseRoots()
	rootFound := false
	for _, captured := range snapshot.Roots {
		if captured.Path == root {
			rootFound = true
		}
	}
	if !rootFound {
		return "", fmt.Errorf("root %q is outside snapshot %s", root, id)
	}
	dir, err := os.MkdirTemp("", "paintedwolf-scan-")
	if err != nil {
		return "", err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(dir)
		}
	}()
	writeEntry := func(entry Entry) error {
		if entry.RootPath != root {
			return nil
		}
		if !filepath.IsLocal(filepath.FromSlash(entry.Path)) {
			return fmt.Errorf("invalid snapshot path %q", entry.Path)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return s.materializeEntry(ctx, dir, entry)
	}
	if paths == nil {
		err = s.ForEachEntry(ctx, id, writeEntry)
	} else {
		for _, rel := range paths {
			entry, found, lookupErr := s.Lookup(ctx, id, root, rel)
			if lookupErr != nil {
				err = lookupErr
				break
			}
			if !found {
				err = fmt.Errorf("snapshot target %q is unavailable", rel)
				break
			}
			if err = writeEntry(entry); err != nil {
				break
			}
		}
	}
	if err != nil {
		return "", err
	}
	success = true
	return dir, nil
}
