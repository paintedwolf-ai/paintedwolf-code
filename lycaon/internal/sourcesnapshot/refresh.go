package sourcesnapshot

import (
	"context"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/repochange"
)

// RefreshNeeded uses watcher state where coverage is complete.
// IsCurrent verifies live content directly.
func (s *Store) RefreshNeeded(ctx context.Context, id string) (bool, error) {
	snapshot, err := s.Get(ctx, id)
	if err != nil {
		return false, err
	}
	for _, root := range snapshot.Roots {
		if !repochange.Coverage(root.Path).Complete {
			current, err := s.IsCurrent(ctx, id)
			return !current, err
		}
		scopeID := s.scopeFor(ctx, Request{}, root).Identity()
		s.deltas.mu.Lock()
		d := s.deltas.roots[filepath.Clean(root.Path)]
		refresh := d == nil || d.covered != id || d.scopeID != scopeID || d.overflow || len(d.paths) > 0
		s.deltas.mu.Unlock()
		if refresh {
			return true, nil
		}
	}
	return false, nil
}

// RefreshPending checks recorded coverage without walking source files. Unknown
// watcher coverage requests an independent capture; it never certifies freshness.
func (s *Store) RefreshPending(ctx context.Context, id string) (bool, error) {
	snapshot, err := s.Get(ctx, id)
	if err != nil {
		return false, err
	}
	for _, root := range snapshot.Roots {
		if !repochange.Coverage(root.Path).Complete {
			return true, nil
		}
		scopeID := s.scopeFor(ctx, Request{}, root).Identity()
		s.deltas.mu.Lock()
		d := s.deltas.roots[filepath.Clean(root.Path)]
		pending := d == nil || d.covered != id || d.scopeID != scopeID || d.overflow || len(d.paths) > 0
		s.deltas.mu.Unlock()
		if pending {
			return true, nil
		}
	}
	return false, nil
}
