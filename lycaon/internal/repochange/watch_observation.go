package repochange

// WatchObservation identifies one uninterrupted directory watch binding.
type WatchObservation struct {
	binding   *worktreeWatcher
	overflows int
}

func (o WatchObservation) Valid() bool { return o.binding != nil }

// ObserveWatch returns no proof for an unwatched or interrupted directory.
func ObserveWatch(root, dir string) WatchObservation { return globalWatchers.observeWatch(root, dir) }

func (r *WatcherRegistry) observeWatch(root, dir string) WatchObservation {
	if r == nil {
		return WatchObservation{}
	}
	key := canonicalDir(root)
	target := watchTarget(key, dir)
	if key == "" || target == "" {
		return WatchObservation{}
	}
	r.mu.Lock()
	w := r.byDir[key]
	r.mu.Unlock()
	if !w.watchesDir(target) {
		return WatchObservation{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.faulted || w.overflowed {
		return WatchObservation{}
	}
	return WatchObservation{binding: w, overflows: w.overflows}
}
