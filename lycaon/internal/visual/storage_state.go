package visual

import "sync"

const maxIdleArtifactRepairStates = 32

type projectStorageLockRegistry struct {
	mu      sync.Mutex
	entries map[string]*projectStorageLock
}

type projectStorageLock struct {
	mu   sync.Mutex
	refs int
}

func (r *projectStorageLockRegistry) lock(key string) func() {
	r.mu.Lock()
	if r.entries == nil {
		r.entries = make(map[string]*projectStorageLock)
	}
	entry := r.entries[key]
	if entry == nil {
		entry = &projectStorageLock{}
		r.entries[key] = entry
	}
	entry.refs++
	r.mu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		r.mu.Lock()
		entry.refs--
		if entry.refs == 0 && r.entries[key] == entry {
			delete(r.entries, key)
		}
		r.mu.Unlock()
	}
}

type artifactRepairRegistry struct {
	mu      sync.Mutex
	entries map[string]*artifactRepairEntry
}

type artifactRepairEntry struct {
	state artifactPruneState
	users int
	drop  bool
}

func (r *artifactRepairRegistry) acquire(key string) (*artifactPruneState, func()) {
	r.mu.Lock()
	if r.entries == nil {
		r.entries = make(map[string]*artifactRepairEntry)
	}
	entry := r.entries[key]
	if entry == nil {
		entry = &artifactRepairEntry{}
		r.entries[key] = entry
	}
	entry.users++
	r.mu.Unlock()
	return &entry.state, func() { r.release(key, entry) }
}

func (r *artifactRepairRegistry) release(key string, entry *artifactRepairEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry.users--
	if entry.users == 0 && (entry.drop || entry.state.dir == nil) {
		entry.state.close()
		if r.entries[key] == entry {
			delete(r.entries, key)
		}
	}
	r.trim()
}

func (r *artifactRepairRegistry) delete(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[key]
	if entry == nil {
		return
	}
	entry.drop = true
	if entry.users == 0 {
		entry.state.close()
		delete(r.entries, key)
	}
}

func (r *artifactRepairRegistry) trim() {
	idle := 0
	for _, entry := range r.entries {
		if entry.users == 0 && entry.state.dir != nil {
			idle++
		}
	}
	for key, entry := range r.entries {
		if idle <= maxIdleArtifactRepairStates {
			return
		}
		if entry.users != 0 || entry.state.dir == nil {
			continue
		}
		entry.state.close()
		delete(r.entries, key)
		idle--
	}
}
