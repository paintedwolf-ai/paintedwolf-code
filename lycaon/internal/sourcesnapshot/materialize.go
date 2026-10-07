package sourcesnapshot

import (
	"context"
	"fmt"
	"os"
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
	tree := materializer{store: s, snapshotID: id, root: root}
	return tree.create(ctx, paths)
}

// materializer owns one disposable execution tree while its snapshot is leased.
type materializer struct {
	store      *Store
	snapshotID string
	root       string
	dir        string
}

func (m *materializer) create(ctx context.Context, paths []string) (string, error) {
	dir, err := os.MkdirTemp("", "paintedwolf-scan-")
	if err != nil {
		return "", err
	}
	m.dir = dir
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(dir)
		}
	}()
	if paths == nil {
		err = m.store.ForEachEntry(ctx, m.snapshotID, func(entry Entry) error { return m.writeEntry(ctx, entry) })
	} else {
		for _, rel := range paths {
			entry, found, lookupErr := m.store.Lookup(ctx, m.snapshotID, m.root, rel)
			if lookupErr != nil {
				err = lookupErr
				break
			}
			if !found {
				err = fmt.Errorf("snapshot target %q is unavailable", rel)
				break
			}
			if err = m.writeEntry(ctx, entry); err != nil {
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
