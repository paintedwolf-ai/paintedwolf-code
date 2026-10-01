package sourcecatalog

import (
	"context"
	"errors"
	"log/slog"
	"path"
	"sort"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/repochange"
)

// Publication serializes writers without holding the mutex used by readers.
func (s *indexStore) acquireStructurePublication(ctx context.Context) (func(), error) {
	s.mu.Lock()
	if s.pins.drained || s.navigation.retired {
		s.mu.Unlock()
		return nil, pagedview.ErrExpired
	}
	if s.publicationGate == nil {
		s.publicationGate = make(chan struct{}, 1)
	}
	gate := s.publicationGate
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	}
}

func (s *indexStore) publishStructure(ctx context.Context, builder *structuralBuilder, expected int64) error {
	fallbackBuilder := builder
	forceRebase := false
	var activeMerged *structuralBuilder
	defer func() {
		if activeMerged != nil {
			activeMerged.close()
		}
	}()
	for {
		current, err := s.retainGeneration(headGeneration, true)
		if err != nil {
			return err
		}
		if current.Generation != expected && forceRebase && !builder.reverseMerged {
			builder.rebased = append(builder.rebased, current)
			if err := builder.rebase(ctx, current.value); err != nil {
				return err
			}
			expected = current.Generation
			forceRebase = false
		} else {
			current.Release()
		}
		next, err := builder.preparePublication(ctx, expected)
		if err != nil {
			return err
		}
		installed, err := s.installPreparedStructure(ctx, next, expected)
		if err != nil {
			return err
		}
		if installed {
			s.structurePublished(ctx)
			return nil
		}
		merged, mergedExpected, mergeErr := s.mergeHeadIntoPrepared(ctx, builder, next, expected)
		if mergeErr == nil {
			if activeMerged != nil {
				activeMerged.close()
			}
			activeMerged = merged
			builder = merged
			expected = mergedExpected
			continue
		}
		next.close()
		if !errors.Is(mergeErr, errStructuralMergeUnavailable) {
			return mergeErr
		}
		if activeMerged != nil {
			activeMerged.close()
			activeMerged = nil
		}
		builder = fallbackBuilder
		expected = builderComparisonID(fallbackBuilder, expected)
		forceRebase = true
	}
}

func builderComparisonID(builder *structuralBuilder, fallback int64) int64 {
	if builder != nil && builder.comparison != nil {
		return builder.comparison.id
	}
	return fallback
}

func (b *structuralBuilder) preparePublication(ctx context.Context, expected int64) (*structuralGeneration, error) {
	if expected >= headGeneration-1 {
		return nil, errors.New("structural generation address space exhausted")
	}
	if err := b.finalize(ctx); err != nil {
		return nil, err
	}
	if b.coverage != nil {
		if err := b.coverage(ctx, b); err != nil {
			return nil, err
		}
		if err := b.finalize(ctx); err != nil {
			return nil, err
		}
	}
	return b.seal(ctx, expected+1)
}

func (s *indexStore) installPreparedStructure(ctx context.Context, next *structuralGeneration, expected int64) (bool, error) {
	release, err := s.acquireStructurePublication(ctx)
	if err != nil {
		next.close()
		return false, err
	}
	defer release()
	s.mu.Lock()
	if s.pins.drained || s.navigation.retired || ctx.Err() != nil {
		s.mu.Unlock()
		next.close()
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return false, pagedview.ErrExpired
	}
	if s.structure.id != expected {
		s.mu.Unlock()
		return false, nil
	}
	// Newer invalidations remain queued; this generation records what was read.
	s.installStructureLocked(next)
	s.mu.Unlock()
	return true, nil
}

const structuralPublicationMergeLimit = 8192

func (s *indexStore) mergeHeadIntoPrepared(ctx context.Context, builder *structuralBuilder, prepared *structuralGeneration, expected int64) (*structuralBuilder, int64, error) {
	current, err := s.retainGeneration(headGeneration, true)
	if err != nil {
		return nil, 0, err
	}
	retainCurrent := false
	defer func() {
		if !retainCurrent {
			current.Release()
		}
	}()
	if current.Generation == expected {
		return nil, 0, errObservationChanged
	}
	previous := builder.comparison
	if previous == nil {
		return nil, 0, errObservationChanged
	}
	merged, err := newStructuralBuilder(s, prepared)
	if err != nil {
		return nil, 0, err
	}
	retained := false
	defer func() {
		if !retained {
			merged.close()
		}
	}()
	merged.comparison = current.value
	merged.coverage = builder.coverage
	merged.reverseMerged = true
	if merged.extra == nil {
		merged.extra = make(map[uint32]*structuralSegment)
	}
	for id, segment := range current.value.segments {
		if _, found := prepared.segments[id]; !found {
			segment.retain()
			merged.extra[id] = segment
		}
	}
	changedCompleteParents := make(map[string]struct{})
	err = visitDirectoryIndexDiff(ctx, previous.directories, current.value.directories, structuralPublicationMergeLimit, func(key string, _ bool, _ structuralDirectory, newFound bool, value structuralDirectory) error {
		if !newFound {
			if err := merged.directories.DeleteSubtree(ctx, key); err != nil && !errors.Is(err, pagedview.ErrMissing) {
				return err
			}
			parent := path.Dir(key)
			if key == "." {
				parent = "."
			}
			return merged.directories.UpdateFlags(ctx, parent, directoryDirty|directoryRepair, 0)
		}
		prepared, found, err := merged.directories.Get(ctx, key)
		if err != nil {
			return err
		}
		if found && (prepared.observation.Complete || prepared.observation.Failure != "") && !value.observation.Complete && value.observation.Failure == "" {
			return merged.directories.UpdateFlags(ctx, key, directoryDirty|directoryRepair, 0)
		}
		if found && value.observation.Complete {
			changedCompleteParents[key] = struct{}{}
		}
		if found {
			if err := merged.directories.SetPrevious(ctx, key, prepared); err != nil {
				return err
			}
		}
		if err := merged.directories.Set(ctx, key, value); err != nil {
			return err
		}
		return merged.directories.UpdateFlags(ctx, key, directoryDirty|directoryRepair, 0)
	})
	if err != nil {
		return nil, 0, err
	}
	err = retirePreparedChildrenFromChangedParents(ctx, merged, current.value, prepared.directories, changedCompleteParents)
	if err != nil {
		return nil, 0, err
	}
	merged.ownedBases = append(merged.ownedBases, prepared)
	merged.rebased = append(merged.rebased, current)
	retainCurrent = true
	retained = true
	return merged, current.Generation, nil
}

func retirePreparedChildrenFromChangedParents(ctx context.Context, builder *structuralBuilder, current *structuralGeneration, prepared structuralDirectoryIndex, parents map[string]struct{}) error {
	for parent := range parents {
		if err := retirePreparedChildrenFromChangedParent(ctx, builder, current, parent, prepared); err != nil {
			return err
		}
	}
	return nil
}

func retirePreparedChildrenFromChangedParent(ctx context.Context, builder *structuralBuilder, current *structuralGeneration, parent string, prepared structuralDirectoryIndex) error {
	currentParent, found, err := current.directories.Get(ctx, parent)
	if err != nil || !found || !currentParent.observation.Complete {
		return err
	}
	currentChildren := pagedview.RangeIndex[TreeItem]{Store: current, Root: currentParent.page}
	return prepared.VisitChildren(ctx, parent, func(value structuralDirectory) error {
		child := value.observation.Path
		currentChild, _, err := currentChildren.LocateItem(ctx, DirectoryOrder(path.Base(child), true))
		if err != nil && !errors.Is(err, pagedview.ErrMissing) {
			return err
		}
		if err == nil && !currentChild.Value.Symlink {
			return nil
		}
		if err := builder.directories.DeleteSubtree(ctx, child); err != nil && !errors.Is(err, pagedview.ErrMissing) {
			return err
		}
		return nil
	})
}

// Unrelated foreground publications survive a bulk scan without restarting traversal.
func (b *structuralBuilder) rebase(ctx context.Context, current *structuralGeneration) error {
	b.finalized = false
	previousWorkspace := b.directories
	directories, err := newStructuralDirectoryWorkspace(previousWorkspace.dir, current.directories)
	if err != nil {
		return err
	}
	retained := false
	defer func() {
		if !retained {
			_ = directories.Close()
		}
	}()
	retirements := pagedview.NewCache[string, bool](1024, 128<<10)
	err = previousWorkspace.VisitFlags(ctx, directoryDirty, false, func(dir string, flags uint64) error {
		record, found, err := previousWorkspace.Get(ctx, dir)
		if err != nil || !found {
			return err
		}
		retired, err := b.foregroundRetired(ctx, current, dir, retirements)
		if err != nil {
			return err
		}
		if retired {
			return directories.DeleteSubtree(ctx, dir)
		}
		var previous structuralDirectory
		if b.comparison != nil {
			previous, _, err = b.comparison.directories.Get(ctx, dir)
			if err != nil {
				return err
			}
		}
		live, _, err := current.directories.Get(ctx, dir)
		if err != nil {
			return err
		}
		if live.observation != previous.observation && (live.observation.Complete || live.observation.Failure != "") {
			flags &^= directoryBranchesKnown | directoryHasBranches
		} else if err := directories.Set(ctx, dir, record); err != nil {
			return err
		}
		return directories.UpdateFlags(ctx, dir, flags|directoryDirty|directoryRepair, 0)
	})
	if err != nil {
		return err
	}
	if b.extra == nil {
		b.extra = make(map[uint32]*structuralSegment)
	}
	if b.base != nil {
		for id, segment := range b.base.segments {
			if _, found := b.extra[id]; !found {
				segment.retain()
				b.extra[id] = segment
			}
		}
	}
	b.base = current
	b.comparison = current
	b.directories = directories
	retained = true
	return previousWorkspace.Close()
}

// Only newer complete memberships can retire a directory observed by another reader.
func (b *structuralBuilder) foregroundRetired(ctx context.Context, current *structuralGeneration, dir string, memo *pagedview.Cache[string, bool]) (bool, error) {
	var trail []string
	ancestor := dir
	retired := false
	for ancestor != "." {
		if cached, known := memo.Get(ancestor); known {
			retired = cached
			break
		}
		trail = append(trail, ancestor)
		ancestor = path.Dir(ancestor)
	}
	for i := len(trail) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		child := trail[i]
		if !retired {
			var err error
			retired, err = b.foregroundEdgeRetired(ctx, current, child)
			if err != nil {
				return false, err
			}
		}
		memo.Put(child, retired, int64(64+len(child)))
	}
	return retired, nil
}

func (b *structuralBuilder) foregroundEdgeRetired(ctx context.Context, current *structuralGeneration, child string) (bool, error) {
	parent := path.Dir(child)
	live, found, err := current.directories.Get(ctx, parent)
	if err != nil || !found || !live.observation.Complete {
		return false, err
	}
	var previous structuralDirectory
	if b.comparison != nil {
		previous, _, err = b.comparison.directories.Get(ctx, parent)
		if err != nil {
			return false, err
		}
	}
	if live.observation == previous.observation {
		return false, nil
	}
	index := pagedview.RangeIndex[TreeItem]{Store: current, Root: live.page}
	item, _, err := index.LocateItem(ctx, DirectoryOrder(path.Base(child), true))
	if errors.Is(err, pagedview.ErrMissing) || err == nil && item.Value.Symlink {
		return true, nil
	}
	return false, err
}

func (s *indexStore) buildStructure(ctx context.Context, dirty map[string]struct{}, recursive bool) error {
	started := time.Now()
	root, release, err := s.acquireNavigation(ctx)
	if err != nil {
		return err
	}
	defer release()
	pin, err := s.retainGeneration(headGeneration, true)
	if err != nil {
		return err
	}
	defer pin.Release()
	base := pin.value
	_, full := dirty["."]
	full = full && recursive
	if base.directories.Len() == 0 {
		full = true
	}
	if full {
		base = nil
		dirty = map[string]struct{}{".": {}}
	}
	builder, err := newStructuralBuilder(s, base)
	if err != nil {
		return err
	}
	// A lane boundary replaces builder, so close whichever one is current.
	defer func() { builder.close() }()
	builder.comparison = pin.value
	expected := pin.Generation
	options := structuralScanOptions{store: s, broker: s.catalog.broker, epoch: repochange.CurrentEpoch(s.root.Path)}
	options.request = func(dir string) backgroundwork.Request {
		request := s.observationRequest(dir)
		request.Interests = &s.inventory.interests
		return request
	}
	options.descend = func(dir string) (bool, error) {
		if recursive || base == nil {
			return true, nil
		}
		record, found, err := base.directories.Get(ctx, dir)
		if err != nil {
			return false, err
		}
		return !found || !record.observation.Complete && record.observation.Failure == "" || record.observation.Invalidation != s.observationMark(dir), nil
	}
	if full {
		// Publishing at the lane boundary settles recursive demand in the trees
		// an expansion opens while collapsed ones are still being read. A pass
		// has one boundary.
		published := false
		options.laneBoundary = func(ctx context.Context) error {
			if published {
				return nil
			}
			next, generation, err := s.publishAtLaneBoundary(ctx, builder, expected)
			if err != nil {
				return err
			}
			builder.close()
			builder, expected, published = next, generation, true
			return nil
		}
	}
	for _, dir := range structuralScanRoots(dirty) {
		if err := scanStructureRoot(ctx, root, dir, options, func(listing directoryDiscovery) error {
			if listing.observation.Failure != "" {
				return builder.retainFailedDirectory(ctx, listing.observation)
			}
			return builder.observe(ctx, listing)
		}); err != nil {
			return err
		}
	}
	// Coverage completion scans inside the builder being published, so its
	// frontier publishes no lane of its own.
	options.laneBoundary = nil
	builder.coverage = func(ctx context.Context, target *structuralBuilder) error {
		return target.completeCoverage(ctx, root, options)
	}
	scanned := time.Now()
	err = s.publishStructure(ctx, builder, expected)
	slog.DebugContext(ctx, "Structural discovery finished", "root", s.root.ID, "directories", builder.directories.Len(),
		"scan_ms", scanned.Sub(started).Milliseconds(),
		"publish_ms", time.Since(scanned).Milliseconds(), "encoded_bytes", builder.writer.TotalBytes(), "error", err)
	return err
}

// Partial boundary publications serve expansions while retaining the last complete structure.
func (s *indexStore) publishAtLaneBoundary(ctx context.Context, builder *structuralBuilder, expected int64) (*structuralBuilder, int64, error) {
	started := time.Now()
	if err := s.publishStructure(ctx, builder, expected); err != nil {
		return nil, 0, err
	}
	head, err := s.retainGeneration(headGeneration, true)
	if err != nil {
		return nil, 0, err
	}
	next, err := newStructuralBuilder(s, head.value)
	if err != nil {
		head.Release()
		return nil, 0, err
	}
	// The pin holds the boundary generation's segments until the continuation closes.
	next.rebased = append(next.rebased, head)
	slog.DebugContext(ctx, "Structural lane boundary published", "root", s.root.ID, "directories", head.value.directories.Len(),
		"publish_ms", time.Since(started).Milliseconds(), "encoded_bytes", builder.writer.TotalBytes())
	return next, head.Generation, nil
}

// Parent-first roots remove redundant descendant rescans.
func structuralScanRoots(dirty map[string]struct{}) []string {
	normalized := make(map[string]struct{}, len(dirty))
	for dir := range dirty {
		dir = normalizeDir(dir)
		if dir == "." {
			return []string{"."}
		}
		normalized[dir] = struct{}{}
	}
	roots := make([]string, 0, len(normalized))
	for dir := range normalized {
		covered := false
		for parent := path.Dir(dir); parent != "."; parent = path.Dir(parent) {
			if _, found := normalized[parent]; found {
				covered = true
				break
			}
		}
		if !covered {
			roots = append(roots, dir)
		}
	}
	sort.Strings(roots)
	return roots
}

// AwaitSubtree waits for caller-defined coverage, independently of inventory completion.
func (c *Catalog) AwaitSubtree(ctx context.Context, project string, root Root, dir string, settled func(context.Context, *Navigation) (bool, error)) error {
	if err := validateObservationDirectory(root, dir); err != nil {
		return err
	}
	store, err := c.indexStore(ctx, project, root)
	if err != nil {
		return err
	}
	return c.awaitStructure(ctx, project, root, store, normalizeDir(dir), settled)
}

// SubtreeCovered reports whether every directory under dir has been listed.
func SubtreeCovered(ctx context.Context, navigation *Navigation, dir string) (bool, error) {
	children, err := navigation.Children(ctx, dir)
	if err != nil {
		return false, err
	}
	state, err := navigation.State(ctx, dir)
	if err != nil {
		return false, err
	}
	unresolved, err := directoryUnresolved(ctx, children, state)
	return unresolved == 0, err
}

// awaitStructure retries settled against the head generation, then against the
// last complete generation published since the call began, until it reports
// done. Between attempts it asks the inventory to cover dir when it is idle.
func (c *Catalog) awaitStructure(ctx context.Context, project string, root Root, store *indexStore, dir string, settled func(context.Context, *Navigation) (bool, error)) error {
	if err := store.startInventory(ctx); err != nil {
		return err
	}
	basis, err := store.retainGeneration(headGeneration, true)
	if err != nil {
		return err
	}
	required := basis.Generation
	basis.Release()
	releaseInterest := store.inventory.interests.Add(backgroundwork.PriorityInteractive)
	defer releaseInterest()
	var passError error
	for {
		store.mu.Lock()
		changed, done := store.inventory.changed, store.inventory.done
		store.mu.Unlock()
		head, err := openNavigation(ctx, store, headGeneration)
		if err != nil {
			return err
		}
		ok, err := settled(ctx, head)
		_ = head.Close()
		if err != nil || ok {
			return err
		}
		completed, completedErr := c.OpenCompletedNavigation(ctx, project, root)
		if completedErr == nil {
			if completed.Generation >= required {
				ok, err = settled(ctx, completed)
			}
			_ = completed.Close()
			if err != nil || ok {
				return err
			}
		} else if !errors.Is(completedErr, pagedview.ErrPreparing) {
			return completedErr
		}
		if passError != nil {
			return passError
		}
		store.mu.Lock()
		if !store.inventory.running {
			if store.inventory.dirty == nil {
				store.inventory.dirty = make(map[string]struct{})
			}
			store.inventory.dirty[dir] = struct{}{}
			select {
			case store.inventory.wake <- struct{}{}:
			default:
			}
		}
		store.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return pagedview.ErrExpired
		case <-changed:
		}
		store.mu.Lock()
		passError = store.inventory.passError
		store.mu.Unlock()
	}
}
