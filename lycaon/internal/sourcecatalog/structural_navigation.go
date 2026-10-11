package sourcecatalog

import (
	"context"
	"errors"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
)

// Completed structure remains available when foreground listings advance only part of a tree.
func (c *Directories) OpenCompletedNavigation(ctx context.Context, project string, root Root) (*Navigation, error) {
	store, err := c.trees.indexStore(ctx, project, root)
	if err != nil {
		return nil, err
	}
	pin, err := store.retainCompletedGeneration()
	if errors.Is(err, pagedview.ErrMissing) {
		return nil, pagedview.ErrPreparing
	}
	if err != nil {
		return nil, err
	}
	defer pin.Release()
	return pin.OpenNavigation(ctx)
}

func (s *indexStore) releaseCompletedStructureLocked() {
	if s.completed != nil {
		s.completed.close()
		s.completed = nil
	}
}

// RequestCoverage schedules reconciliation without making readers wait for it.
func (c *Directories) RequestCoverage(ctx context.Context, project string, root Root, dir string) error {
	if err := validateObservationDirectory(root, dir); err != nil {
		return err
	}
	store, err := c.trees.indexStore(ctx, project, root)
	if err != nil {
		return err
	}
	if err := store.startInventory(ctx); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.inventory.dirty == nil {
		store.inventory.dirty = make(map[string]struct{})
	}
	store.inventory.dirty[normalizeDir(dir)] = struct{}{}
	select {
	case store.inventory.wake <- struct{}{}:
	default:
	}
	return nil
}

// Publication fixes parent memberships before this walk fills their unknown branches.
func (b *structuralBuilder) completeCoverage(ctx context.Context, root *os.Root, options structuralScanOptions) error {
	frontier := structuralScanQueue{dir: structuralScanSpoolDir(options)}
	defer frontier.close()
	if err := frontier.push([]string{"."}); err != nil {
		return err
	}
	for frontier.len() > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir, err := frontier.pop()
		if err != nil {
			return err
		}
		children, observation, err := b.children(ctx, dir)
		if err != nil {
			return err
		}
		if observation.Failure != "" {
			continue
		}
		if !observation.Complete {
			if err := b.collectMissingCoverage(ctx, root, dir, options); err != nil {
				return err
			}
			children, observation, err = b.children(ctx, dir)
			if err != nil {
				return err
			}
			if observation.Failure != "" {
				continue
			}
		}
		if err := children.VisitUnresolved(ctx, func(item pagedview.RangeItem[TreeItem]) (bool, error) {
			if !directoryOrderKind(item.Key) || item.Value.Symlink || b.policy.boundaryDir(item.Value.Path) != "" {
				return true, nil
			}
			return true, frontier.push([]string{item.Value.Path})
		}); err != nil {
			return err
		}
	}
	return nil
}

func (b *structuralBuilder) collectMissingCoverage(ctx context.Context, root *os.Root, dir string, options structuralScanOptions) error {
	var workspace sync.Mutex
	options.descend = func(child string) (bool, error) {
		workspace.Lock()
		defer workspace.Unlock()
		return b.admitMissingCoverage(ctx, child)
	}
	admitted, err := b.admitMissingCoverage(ctx, dir)
	if err != nil || !admitted {
		return err
	}
	return scanStructureRoot(ctx, root, dir, options, func(listing directoryDiscovery) error {
		workspace.Lock()
		defer workspace.Unlock()
		if listing.observation.Failure != "" {
			return b.retainFailedDirectory(ctx, listing.observation)
		}
		if err := b.observe(ctx, listing); err != nil {
			return err
		}
		if listing.observation.Complete {
			return b.prune(ctx, listing.observation.Path)
		}
		return nil
	})
}

func (b *structuralBuilder) admitMissingCoverage(ctx context.Context, dir string) (bool, error) {
	record, found, err := b.directories.Get(ctx, dir)
	if err != nil {
		return false, err
	}
	if found && (record.observation.Complete || record.observation.Failure != "") {
		return false, nil
	}
	flags, err := b.directories.Flags(ctx, dir)
	if err != nil || flags&directoryCoverageQueued != 0 {
		return false, err
	}
	if found {
		if err := b.directories.SetPrevious(ctx, dir, record); err != nil {
			return false, err
		}
	}
	err = b.directories.UpdateFlags(ctx, dir, directoryCoverageQueued, directoryStarted|directoryBranchesKnown|directoryHasBranches)
	return err == nil, err
}

func scanStructureRoot(ctx context.Context, root *os.Root, dir string, options structuralScanOptions, emit func(directoryDiscovery) error) error {
	observation := structuralScanObservation(dir, options)
	scanOptions := options
	scanOptions.mark = func(child string) uint64 {
		if child == dir {
			return observation.Invalidation
		}
		return structuralScanMark(options, child)
	}
	err := scanStructure(ctx, root, dir, scanOptions, emit)
	if err == nil {
		return nil
	}
	var pathError *os.PathError
	filesystemFailure := errors.As(err, &pathError) || errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrInvalid)
	if dir == "." || ctx.Err() != nil || errors.Is(err, errStructuralScanControl) || !filesystemFailure {
		return err
	}
	return emit(structuralScanFailure(observation, err))
}

func (b *structuralBuilder) retainFailedDirectory(ctx context.Context, observation DirectoryObservation) error {
	index, previous, err := b.children(ctx, observation.Path)
	if err != nil {
		return err
	}
	observation.Sequence = structuralObservationSerial.Add(1)
	observation.FirstListed = observation.Sequence
	observation.Entries = previous.Entries
	observation.Observed = time.Now()
	b.finalized = false
	if err := b.directories.UpdateFlags(ctx, observation.Path, directoryDirty|directoryRepair, 0); err != nil {
		return err
	}
	return b.save(ctx, index, observation)
}

// Boundaries are a fact about one generation, so disclosure commands under a
// standing recursive rule share one walk of it.
type collapseCache struct {
	mu         sync.Mutex
	generation int64
	answers    map[string][]string
}

// Anchors are few; a new generation replaces the whole set.
const maxCachedCollapseAnchors = 64

func (s *collapseCache) cachedBoundaries(generation int64, dir string) ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != generation {
		return nil, false
	}
	boundaries, found := s.answers[dir]
	return boundaries, found
}

func (s *collapseCache) cacheBoundaries(generation int64, dir string, boundaries []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != generation || s.answers == nil {
		s.generation = generation
		s.answers = make(map[string][]string)
	}
	if len(s.answers) >= maxCachedCollapseAnchors {
		return
	}
	s.answers[dir] = boundaries
}

// CollapseBoundaries returns outermost collapsed directories without waiting.
// Ready is false while an expanded directory remains unlisted.
func (c *Directories) CollapseBoundaries(ctx context.Context, project string, root Root, dir string) ([]string, bool, error) {
	if err := validateObservationDirectory(root, dir); err != nil {
		return nil, false, err
	}
	store, err := c.trees.indexStore(ctx, project, root)
	if err != nil {
		return nil, false, err
	}
	if err := store.startInventory(ctx); err != nil {
		return nil, false, err
	}
	policy := c.trees.policyFor(ctx, root.Path)
	dir = normalizeDir(dir)
	navigation, err := openNavigation(ctx, store, headGeneration)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = navigation.Close() }()
	if cached, found := store.collapse.cachedBoundaries(navigation.Generation, dir); found {
		return cached, true, nil
	}
	found, ready, err := collapseBoundariesUnder(ctx, navigation, policy, dir)
	if err != nil || !ready {
		return nil, false, err
	}
	store.collapse.cacheBoundaries(navigation.Generation, dir, found)
	return found, true, nil
}

// An unfinished listing makes the entire boundary result not ready.
func collapseBoundariesUnder(ctx context.Context, navigation *Navigation, policy walkPolicy, dir string) ([]string, bool, error) {
	boundaries := []string{}
	explicitBoundary := policy.collapseDir(dir) || policy.boundaryPath(dir, true) != ""
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
				if policy.boundaryDir(item.Value.Path) != "" || !explicitBoundary && policy.collapseDir(item.Value.Path) {
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
