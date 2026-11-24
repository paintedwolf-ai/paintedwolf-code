package sourcecatalog

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/repochange"
)

// StructuralMetrics counts value-free physical discovery, encoding, publication, and compaction activity.
type StructuralMetrics struct {
	PhysicalDirectoryReads int64
	EncodedBytes           int64
	BoundaryEncodes        int64
	LanePublications       int64
	HeadPublications       int64
	CompactionAttempts     int64
	CompactionInstalls     int64
	CompactionDeclines     int64
}

type structuralMetrics struct {
	physicalDirectoryReads atomic.Int64
	encodedBytes           atomic.Int64
	boundaryEncodes        atomic.Int64
	lanePublications       atomic.Int64
	headPublications       atomic.Int64
	compactionAttempts     atomic.Int64
	compactionInstalls     atomic.Int64
	compactionDeclines     atomic.Int64
}

// Metrics returns cumulative structural discovery, encoding, publication, and compaction counters.
func (c *Directories) Metrics() StructuralMetrics {
	if c == nil {
		return StructuralMetrics{}
	}
	m := &c.metrics
	return StructuralMetrics{
		PhysicalDirectoryReads: m.physicalDirectoryReads.Load(),
		EncodedBytes:           m.encodedBytes.Load(),
		BoundaryEncodes:        m.boundaryEncodes.Load(),
		LanePublications:       m.lanePublications.Load(),
		HeadPublications:       m.headPublications.Load(),
		CompactionAttempts:     m.compactionAttempts.Load(),
		CompactionInstalls:     m.compactionInstalls.Load(),
		CompactionDeclines:     m.compactionDeclines.Load(),
	}
}

func (s *indexStore) structuralMetrics() *structuralMetrics {
	if s == nil || s.stores == nil || s.stores.Directories == nil {
		return nil
	}
	return &s.stores.Directories.metrics
}

func (s *indexStore) recordPhysicalDirectoryRead(dir string) {
	if s == nil || s.stores == nil || s.stores.Directories == nil {
		return
	}
	d := s.stores.Directories
	d.metrics.physicalDirectoryReads.Add(1)
	if hook := d.onScanDirectory; hook != nil {
		hook(s.root.Path, dir)
	}
}

func (s *indexStore) recordPublication(complete bool) {
	m := s.structuralMetrics()
	if m == nil {
		return
	}
	if complete {
		m.headPublications.Add(1)
	} else {
		m.lanePublications.Add(1)
	}
}

func cloneStructuralGeneration(src *structuralGeneration, id int64) *structuralGeneration {
	segments := make(map[uint32]*structuralSegment, len(src.segments))
	for key, segment := range src.segments {
		segment.retain()
		segments[key] = segment
	}
	cloned := &structuralGeneration{
		id:                  id,
		complete:            src.complete,
		directories:         src.directories,
		segments:            segments,
		encodedBytes:        src.encodedBytes,
		compactionDebtBytes: src.compactionDebtBytes,
		verifiedEpoch:       src.verifiedEpoch,
	}
	cloned.directories.store = newStructuralDirectoryPages(cloned, nil)
	return cloned
}

func (s *indexStore) structureShareKey(ctx context.Context) (string, walkPolicy, bool) {
	if s == nil || s.stores == nil || s.root.OnDemand || s.root.DirectoryIdentity != "" || s.root.Only != "" {
		return "", walkPolicy{}, false
	}
	if s.policy.includeDependencies || len(s.policy.selectedPaths) > 0 || s.policy.base != "" {
		return "", walkPolicy{}, false
	}
	policy := s.stores.policyFor(ctx, s.root.Path).forRoot(s.root)
	if policy.onDemand {
		return "", policy, false
	}
	canon := fspath.CanonicalPath(s.root.Path)
	if canon == "" {
		canon = s.root.Path
	}
	var scopeID string
	if policy.scope != nil {
		scopeID = policy.scope.Identity()
	}
	return fmt.Sprintf("%s\x00%+v\x00%s", canon, policy.budgets, scopeID), policy, true
}

func (c *TreeStores) peerIndexStores(exclude *indexStore) []*indexStore {
	c.mu.Lock()
	defer c.mu.Unlock()
	peers := make([]*indexStore, 0, len(c.trees))
	for _, store := range c.trees {
		if idx, ok := store.(*indexStore); ok && idx != nil && idx != exclude {
			peers = append(peers, idx)
		}
	}
	return peers
}

func (s *indexStore) restorePeerStructure(ctx context.Context) bool {
	s.mu.Lock()
	s.initializeStructureLocked()
	expected := s.structure.id
	empty := s.structure.directories.Len() == 0
	s.mu.Unlock()
	if !empty || !repochange.DirWatched(s.root.Path, ".") {
		return false
	}
	adopted, err := s.adoptPeerCompletedStructure(ctx, expected)
	return err == nil && adopted
}

func (s *indexStore) adoptPeerCompletedStructure(ctx context.Context, expected int64) (bool, error) {
	if !repochange.DirWatched(s.root.Path, ".") {
		return false, nil
	}
	key, policy, ok := s.structureShareKey(ctx)
	if !ok {
		return false, nil
	}
	for _, peer := range s.stores.peerIndexStores(s) {
		peerKey, _, peerOK := peer.structureShareKey(ctx)
		if !peerOK || peerKey != key {
			continue
		}
		peer.mu.Lock()
		var candidate *structuralGeneration
		var peerFullMark uint64
		if !peer.pins.drained && !peer.navigation.Retired() && peer.completed != nil && peer.completed.complete &&
			!peer.inventory.full && len(peer.inventory.dirty) == 0 && len(peer.invalidation.paths) == 0 && len(peer.invalidation.parents) == 0 {
			candidate = cloneStructuralGeneration(peer.completed, expected+1)
			peerFullMark = peer.invalidation.full
		}
		peer.mu.Unlock()
		if candidate == nil {
			continue
		}
		rootDir, found, err := candidate.directories.Get(ctx, ".")
		currentEpoch := repochange.CurrentEpoch(s.root.Path)
		epochVerified := rootDir.observation.Epoch == currentEpoch || (currentEpoch != (repochange.Epoch{}) && candidate.verifiedEpoch == currentEpoch)
		if err != nil || !found || !rootDir.observation.Complete || rootDir.observation.Failure != "" ||
			rootDir.observation.Invalidation < peerFullMark || !epochVerified ||
			!repochange.DirWatched(s.root.Path, ".") {
			candidate.close()
			continue
		}
		installed, installErr := s.installAdoptedStructure(ctx, candidate, expected, rootDir.observation.Invalidation, policy)
		if installErr != nil {
			return false, installErr
		}
		if installed {
			s.structurePublished(ctx)
			return true, nil
		}
	}
	return false, nil
}

func (s *indexStore) installAdoptedStructure(ctx context.Context, next *structuralGeneration, expected int64, baselineMark uint64, policy walkPolicy) (bool, error) {
	if expected >= headGeneration-1 {
		next.close()
		return false, errStructuralGenerationAddress
	}
	release, err := s.acquireStructurePublication(ctx)
	if err != nil {
		next.close()
		return false, err
	}
	defer release()
	s.mu.Lock()
	if s.pins.drained || s.navigation.Retired() || ctx.Err() != nil {
		s.mu.Unlock()
		next.close()
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return false, pagedview.ErrExpired
	}
	if s.structure.id != expected {
		s.mu.Unlock()
		next.close()
		return false, nil
	}
	s.policy = policy
	s.alignInitialInvalidationLocked(baselineMark)
	s.installStructureLocked(next)
	s.mu.Unlock()
	s.recordPublication(next.complete)
	return true, nil
}
