package sourcecatalog

import (
	"context"
	"sort"
)

// Boundaries are a fact about one generation, so disclosure commands under a
// standing recursive rule share one walk of it.
type collapseCache struct {
	generation int64
	answers    map[string][]string
}

// Anchors are few; a new generation replaces the whole set.
const maxCachedCollapseAnchors = 64

func (s *indexStore) cachedBoundaries(generation int64, dir string) ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.collapse.generation != generation {
		return nil, false
	}
	boundaries, found := s.collapse.answers[dir]
	return boundaries, found
}

func (s *indexStore) cacheBoundaries(generation int64, dir string, boundaries []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.collapse.generation != generation || s.collapse.answers == nil {
		s.collapse = collapseCache{generation: generation, answers: make(map[string][]string)}
	}
	if len(s.collapse.answers) >= maxCachedCollapseAnchors {
		return
	}
	s.collapse.answers[dir] = boundaries
}

// CollapseBoundaries returns outermost collapsed directories without waiting.
// Ready is false while an expanded directory remains unlisted.
func (c *Catalog) CollapseBoundaries(ctx context.Context, project string, root Root, dir string) ([]string, bool, error) {
	if err := validateObservationDirectory(root, dir); err != nil {
		return nil, false, err
	}
	store, err := c.indexStore(ctx, project, root)
	if err != nil {
		return nil, false, err
	}
	if err := store.startInventory(ctx); err != nil {
		return nil, false, err
	}
	policy := c.policyFor(ctx, root.Path)
	dir = normalizeDir(dir)
	if policy.collapseDir(dir) {
		return nil, true, nil
	}
	navigation, err := openNavigation(ctx, store, headGeneration)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = navigation.Close() }()
	if cached, found := store.cachedBoundaries(navigation.Generation, dir); found {
		return cached, true, nil
	}
	found, ready, err := collapseBoundariesUnder(ctx, navigation, policy, dir)
	if err != nil || !ready {
		return nil, false, err
	}
	store.cacheBoundaries(navigation.Generation, dir, found)
	return found, true, nil
}

// An unfinished listing makes the entire boundary result not ready.
func collapseBoundariesUnder(ctx context.Context, navigation *Navigation, policy walkPolicy, dir string) ([]string, bool, error) {
	boundaries := []string{}
	pending := []string{dir}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		state, err := navigation.State(ctx, current)
		if err != nil {
			return nil, false, err
		}
		if state.Failure != "" {
			continue
		}
		if !state.Complete {
			return nil, false, nil
		}
		children, err := navigation.Children(ctx, current)
		if err != nil {
			return nil, false, err
		}
		after := ""
		for {
			items, err := children.ReadAfter(ctx, after, indexBatchSize)
			if err != nil {
				return nil, false, err
			}
			if len(items) == 0 {
				break
			}
			directories := true
			for _, item := range items {
				// Directory keys sort first, so the first file ends the scan.
				if !directoryOrderKind(item.Key) {
					directories = false
					break
				}
				if item.Value.Symlink {
					continue
				}
				if policy.collapseDir(item.Value.Path) {
					boundaries = append(boundaries, item.Value.Path)
				} else {
					pending = append(pending, item.Value.Path)
				}
			}
			if !directories {
				break
			}
			after = items[len(items)-1].Key
		}
	}
	sort.Strings(boundaries)
	return boundaries, true, nil
}
