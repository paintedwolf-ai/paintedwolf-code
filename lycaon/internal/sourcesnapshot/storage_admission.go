package sourcesnapshot

import (
	"context"
	"errors"
	"math"

	"golang.org/x/sync/semaphore"
)

// ErrMaintenanceDeferred means active captures still own their source generations.
var ErrMaintenanceDeferred = errors.New("source snapshot maintenance deferred behind capture")

const storageExclusive = math.MaxInt64

func (s *Store) storageAdmission() *semaphore.Weighted {
	s.storageGateOnce.Do(func() { s.storageGate = semaphore.NewWeighted(storageExclusive) })
	return s.storageGate
}

// Publication and deletion coordinate without making cancellation wait on a mutex.
func (s *Store) acquireStorage(ctx context.Context, exclusive bool) (func(), error) {
	weight := int64(1)
	if exclusive {
		weight = storageExclusive
	}
	gate := s.storageAdmission()
	if err := gate.Acquire(ctx, weight); err != nil {
		return nil, err
	}
	return func() { gate.Release(weight) }, nil
}

type rootStorageGate struct {
	gate  *semaphore.Weighted
	users int
}

// Deletion waits for readers of its roots, without waiting for unrelated captures.
func (s *Store) acquireRootStorage(ctx context.Context, roots []Root, exclusive bool) (func(), error) {
	weight := int64(1)
	if exclusive {
		weight = storageExclusive
	}
	var releases []func()
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, root := range normalizeRoots(roots) {
		gate, forget := s.retainRootStorage(root.Path)
		if err := gate.Acquire(ctx, weight); err != nil {
			forget()
			releaseAll()
			return nil, err
		}
		releases = append(releases, func() { gate.Release(weight); forget() })
	}
	return releaseAll, nil
}

func (s *Store) retainRootStorage(root string) (*semaphore.Weighted, func()) {
	s.mu.Lock()
	if s.rootStorage == nil {
		s.rootStorage = make(map[string]*rootStorageGate)
	}
	held := s.rootStorage[root]
	if held == nil {
		held = &rootStorageGate{gate: semaphore.NewWeighted(storageExclusive)}
		s.rootStorage[root] = held
	}
	held.users++
	s.mu.Unlock()
	return held.gate, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		held.users--
		if held.users == 0 {
			delete(s.rootStorage, root)
		}
	}
}
