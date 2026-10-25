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
	view, err := s.open(ctx, id)
	if err != nil {
		return "", err
	}
	defer view.release()
	snapshot := view.Snapshot
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
	tree := materializer{store: s, view: view, root: root}
	return tree.create(ctx, paths)
}

// materializer owns one disposable execution tree while its snapshot is held.
type materializer struct {
	store *Store
	view  *snapshotView
	root  string
	dir   string
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
		err = m.store.forEachManifestEntry(ctx, m.view.manifests[m.root], m.root, func(entry Entry) error { return m.writeEntry(ctx, entry) })
	} else {
		for _, rel := range paths {
			entry, found, lookupErr := m.store.lookup(ctx, m.view, m.root, rel)
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
