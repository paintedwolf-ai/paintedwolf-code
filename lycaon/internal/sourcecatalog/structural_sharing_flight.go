package sourcecatalog

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/repochange"
)

type sharedStructureCoordinator struct {
	mu       sync.Mutex
	inflight map[string]*sharedStructureFlight
}

type sharedStructureFlight struct {
	coord    *sharedStructureCoordinator
	key      string
	epoch    repochange.Epoch
	policy   walkPolicy
	refs     atomic.Int32
	rootRead atomic.Bool

	mu       sync.Mutex
	lane     *structuralGeneration
	laneDone chan struct{}
	laneOnce sync.Once
	head     *structuralGeneration
	done     chan struct{}
	doneOnce sync.Once
}

func (c *sharedStructureCoordinator) beginOrJoin(ctx context.Context, s *indexStore) (*sharedStructureFlight, walkPolicy, bool) {
	key, policy, ok := s.structureShareKey(ctx)
	if !ok {
		return nil, s.policy, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inflight == nil {
		c.inflight = make(map[string]*sharedStructureFlight)
	}
	if existing := c.inflight[key]; existing != nil {
		if existing.epoch == repochange.CurrentEpoch(s.root.Path) && (repochange.DirWatched(s.root.Path, ".") || !existing.rootRead.Load()) {
			existing.refs.Add(1)
			return existing, existing.policy, false
		}
		return nil, policy, false
	}
	flight := &sharedStructureFlight{
		coord:    c,
		key:      key,
		epoch:    repochange.CurrentEpoch(s.root.Path),
		policy:   policy,
		laneDone: make(chan struct{}),
		done:     make(chan struct{}),
	}
	flight.refs.Store(1)
	c.inflight[key] = flight
	return flight, policy, true
}

func (f *sharedStructureFlight) markDirectoryScanned(dir string) {
	if f != nil && dir == "." {
		f.rootRead.Store(true)
	}
}

func (f *sharedStructureFlight) publishLane(src *structuralGeneration) {
	if f == nil || src == nil {
		return
	}
	cloned := cloneStructuralGeneration(src, 0)
	f.mu.Lock()
	if f.lane == nil {
		f.lane = cloned
	} else {
		cloned.close()
	}
	f.mu.Unlock()
	f.laneOnce.Do(func() { close(f.laneDone) })
}

func (f *sharedStructureFlight) publishHeadFrom(s *indexStore) {
	if f == nil || s == nil {
		return
	}
	s.mu.Lock()
	var cloned *structuralGeneration
	if s.completed != nil && s.completed.complete {
		cloned = cloneStructuralGeneration(s.completed, 0)
	}
	s.mu.Unlock()
	if cloned == nil {
		return
	}
	f.mu.Lock()
	if f.head == nil {
		f.head = cloned
	} else {
		cloned.close()
	}
	f.mu.Unlock()
}

func (f *sharedStructureFlight) cloneLane(id int64) *structuralGeneration {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lane == nil {
		return nil
	}
	return cloneStructuralGeneration(f.lane, id)
}

func (f *sharedStructureFlight) cloneHead(id int64) *structuralGeneration {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.head == nil {
		return nil
	}
	return cloneStructuralGeneration(f.head, id)
}

func (f *sharedStructureFlight) finish() {
	if f == nil {
		return
	}
	f.coord.mu.Lock()
	if f.coord.inflight[f.key] == f {
		delete(f.coord.inflight, f.key)
	}
	f.coord.mu.Unlock()
	f.laneOnce.Do(func() { close(f.laneDone) })
	f.doneOnce.Do(func() { close(f.done) })
	f.release()
}

func (f *sharedStructureFlight) release() {
	if f == nil || f.refs.Add(-1) != 0 {
		return
	}
	f.mu.Lock()
	lane, head := f.lane, f.head
	f.lane, f.head = nil, nil
	f.mu.Unlock()
	if lane != nil {
		lane.close()
	}
	if head != nil {
		head.close()
	}
}

func (s *indexStore) inventoryDone() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inventory.done
}

func (s *indexStore) isDrainedOrRetired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pins.drained || s.navigation.Retired()
}

func (s *indexStore) refreshWalkPolicy(ctx context.Context) {
	if s == nil || s.stores == nil || s.policy.includeDependencies || len(s.policy.selectedPaths) > 0 || s.policy.base != "" {
		return
	}
	policy := s.stores.policyFor(ctx, s.root.Path).forRoot(s.root)
	s.mu.Lock()
	s.policy = policy
	s.mu.Unlock()
}

func (s *indexStore) prepareSharedFullBuild(ctx context.Context, expected int64) (bool, *sharedStructureFlight, error) {
	adopted, err := s.adoptPeerCompletedStructure(ctx, expected)
	if adopted || err != nil {
		return true, nil, err
	}
	flight, policy, leader := s.stores.sharedStructure.beginOrJoin(ctx, s)
	if !s.policy.includeDependencies && len(s.policy.selectedPaths) == 0 && s.policy.base == "" {
		s.mu.Lock()
		s.policy = policy
		s.mu.Unlock()
	}
	if flight == nil {
		return false, nil, nil
	}
	if leader {
		return false, flight, nil
	}
	defer flight.release()
	done, joinErr := s.joinSharedStructure(ctx, flight, expected)
	if done || joinErr != nil {
		return true, nil, joinErr
	}
	return false, nil, nil
}

func (s *indexStore) joinSharedStructure(ctx context.Context, flight *sharedStructureFlight, expected int64) (bool, error) {
	done := s.inventoryDone()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-done:
		return false, pagedview.ErrExpired
	case <-flight.laneDone:
	}
	if s.isDrainedOrRetired() {
		return false, pagedview.ErrExpired
	}
	if lane := flight.cloneLane(expected + 1); lane != nil {
		rootDir, found, err := lane.directories.Get(ctx, ".")
		if err != nil {
			lane.close()
			return false, err
		}
		if found {
			installed, installErr := s.installAdoptedStructure(ctx, lane, expected, rootDir.observation.Invalidation, flight.policy)
			if installErr != nil {
				return false, installErr
			}
			if installed {
				expected = lane.id
				s.structurePublished(ctx)
			}
		} else {
			lane.close()
		}
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-done:
		return false, pagedview.ErrExpired
	case <-flight.done:
	}
	if s.isDrainedOrRetired() {
		return false, pagedview.ErrExpired
	}
	head := flight.cloneHead(expected + 1)
	if head == nil {
		return false, nil
	}
	rootDir, found, err := head.directories.Get(ctx, ".")
	if err != nil || !found {
		head.close()
		return false, err
	}
	installed, installErr := s.installAdoptedStructure(ctx, head, expected, rootDir.observation.Invalidation, flight.policy)
	if installErr != nil {
		return false, installErr
	}
	if !installed {
		return false, nil
	}
	s.structurePublished(ctx)
	return true, nil
}
