package repochange

import (
	"path/filepath"
	"sync/atomic"
)

type watchRegistration struct {
	cost     int
	identity uint64
	coverage uint64
}

var watchRegistrationSequence atomic.Uint64

func newWatchRegistration(cost int) watchRegistration {
	identity := watchRegistrationSequence.Add(1)
	return watchRegistration{cost: cost, identity: identity, coverage: identity}
}

// WatchObservation identifies one uninterrupted directory watch registration.
type WatchObservation struct {
	registration uint64
	overflows    int
}

// Valid reports whether a live registration supplied the observation.
func (o WatchObservation) Valid() bool { return o.registration != 0 }

// ObserveWatch returns no proof for an unwatched or interrupted directory.
func ObserveWatch(root, dir string) WatchObservation { return globalWatchers.observeWatch(root, dir) }

func (r *WatcherRegistry) observeWatch(root, dir string) WatchObservation {
	if r == nil {
		return WatchObservation{}
	}
	key := canonicalDir(root)
	if key == "" {
		return WatchObservation{}
	}
	target := watchTarget(key, dir)
	if target == "" {
		return WatchObservation{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.byDir[key]
	if w == nil {
		return WatchObservation{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	registration, ok := w.directoryRegistrationLocked(target)
	if !ok || w.faulted || w.overflowed {
		return WatchObservation{}
	}
	return WatchObservation{registration: registration.identity, overflows: w.overflows}
}

func (w *worktreeWatcher) directoryRegistrationLocked(target string) (watchRegistration, bool) {
	if w.scope != nil {
		rel, err := filepath.Rel(w.root, target)
		if err != nil || w.scope.BoundaryPath(filepath.ToSlash(rel), true) != "" {
			return watchRegistration{}, false
		}
	}
	if w.recursive {
		if !w.coveredByRootLocked(target) {
			return watchRegistration{}, false
		}
		target = w.root
	}
	registration, ok := w.watched[target]
	return registration, ok
}

// WatchScopeObservation identifies directory coverage available to preparation.
type WatchScopeObservation struct {
	registration uint64
	coverage     uint64
	overflows    int
}

// ObserveWatchScope changes when any registration in a root changes.
func ObserveWatchScope(root string) WatchScopeObservation {
	return globalWatchers.observeWatchScope(root)
}

func (r *WatcherRegistry) observeWatchScope(root string) WatchScopeObservation {
	if r == nil {
		return WatchScopeObservation{}
	}
	key := canonicalDir(root)
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.byDir[key]
	if w == nil {
		return WatchScopeObservation{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	registration, ok := w.directoryRegistrationLocked(w.root)
	if !ok || w.faulted || w.overflowed {
		return WatchScopeObservation{}
	}
	return WatchScopeObservation{registration: registration.identity, coverage: registration.coverage, overflows: w.overflows}
}

func (w *worktreeWatcher) registerDirectoryLocked(path string, cost int) {
	w.watched[path] = newWatchRegistration(cost)
	w.advanceCoverageLocked()
}

func (w *worktreeWatcher) removeDirectoryLocked(path string) {
	delete(w.watched, path)
	w.advanceCoverageLocked()
}

func (w *worktreeWatcher) advanceCoverageLocked() {
	if root, ok := w.watched[w.root]; ok {
		root.coverage = watchRegistrationSequence.Add(1)
		w.watched[w.root] = root
	}
}
