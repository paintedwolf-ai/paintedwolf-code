package pagedview

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Scope binds an opaque handle to the authorized person and source boundary.
type Scope struct{ Person, Project, Workspace string }

type retained[T any] struct {
	scope   Scope
	value   T
	used    time.Time
	pins    int
	removed bool
	charge  *Reservation
	dispose func(T)
}

// Registry retains handles by opaque ID. A handle it does not retain for the
// caller's scope — expired, released, evicted, from another scope, or issued
// before a restart — answers ErrExpired; callers reopen instead of guessing why.
type Registry[T any] struct {
	mu         sync.Mutex
	entries    map[string]*retained[T]
	budget     *Budget
	ttl        time.Duration
	capacity   int
	clock      func() time.Time
	closed     bool
	leased     bool
	disposal   []*retained[T]
	unregister func()
}

func NewRegistry[T any](budget *Budget, capacity int, ttl time.Duration) *Registry[T] {
	return newRegistry[T](budget, capacity, ttl, false)
}

func newRegistry[T any](budget *Budget, capacity int, ttl time.Duration, leased bool) *Registry[T] {
	registry := &Registry[T]{leased: leased, entries: make(map[string]*retained[T]), budget: budget, capacity: max(1, capacity), ttl: ttl, clock: time.Now}
	registry.unregister = budget.registerEvictor(registry.idleCandidate)
	return registry
}

// NewLeaseRegistry expires abandoned resources without evicting live leases for capacity.
func NewLeaseRegistry[T any](budget *Budget, capacity int, ttl time.Duration) *Registry[T] {
	return newRegistry[T](budget, capacity, ttl, true)
}

func (r *Registry[T]) Put(scope Scope, value T, bytes int64, dispose func(T)) (string, error) {
	id, _, err := r.put(scope, value, bytes, dispose, false)
	return id, err
}

// PutPinned reserves and pins in one step, before preparation allocates memory.
func (r *Registry[T]) PutPinned(scope Scope, value T, bytes int64, dispose func(T)) (string, func(), error) {
	return r.put(scope, value, bytes, dispose, true)
}

func (r *Registry[T]) put(scope Scope, value T, bytes int64, dispose func(T), pinned bool) (string, func(), error) {
	if !r.budget.canFit(bytes) {
		return "", nil, ErrBudget
	}
	for {
		r.mu.Lock()
		if r.closed {
			r.unlock()
			return "", nil, ErrExpired
		}
		r.expireLocked()
		if len(r.disposal) > 0 {
			r.unlock()
			continue
		}
		if len(r.entries) >= r.capacity {
			evicted := r.evictLocked()
			r.unlock()
			if !evicted {
				return "", nil, ErrBudget
			}
			continue
		}
		charge, err := r.budget.Reserve(bytes)
		if err != nil {
			r.unlock()
			if !r.budget.evictIdle() {
				return "", nil, err
			}
			continue
		}
		id := uuid.NewString()
		entry := &retained[T]{scope: scope, value: value, used: r.clock(), charge: charge, dispose: dispose}
		r.entries[id] = entry
		var release func()
		if pinned {
			entry.pins = 1
			var once sync.Once
			release = func() { once.Do(func() { r.unpin(entry) }) }
		}
		r.unlock()
		return id, release, nil
	}
}

// Resize adjusts a pinned resource after a preparation phase, preserving active readers.
func (r *Registry[T]) Resize(scope Scope, id string, bytes int64) error {
	if !r.budget.canFit(bytes) {
		return ErrBudget
	}
	for {
		r.mu.Lock()
		entry := r.entries[id]
		if entry == nil || entry.scope != scope {
			r.unlock()
			return ErrExpired
		}
		if entry.pins == 0 {
			r.unlock()
			return ErrExpired
		}
		err := entry.charge.Resize(bytes)
		r.unlock()
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrBudget) || !r.budget.evictIdle() {
			return err
		}
	}
}

// Acquire pins the resource until release. Releasing a view cannot invalidate an active read.
func (r *Registry[T]) Acquire(scope Scope, id string) (T, func(), error) {
	r.mu.Lock()
	defer r.unlock()
	r.expireLocked()
	entry := r.entries[id]
	if entry == nil || entry.scope != scope {
		var zero T
		return zero, nil, ErrExpired
	}
	entry.pins++
	entry.used = r.clock()
	var once sync.Once
	return entry.value, func() { once.Do(func() { r.unpin(entry) }) }, nil
}

// Release is idempotent: a handle the scope does not retain is already released.
func (r *Registry[T]) Release(scope Scope, id string) {
	r.mu.Lock()
	defer r.unlock()
	r.expireLocked()
	if entry := r.entries[id]; entry != nil && entry.scope == scope {
		r.removeLocked(id, entry)
	}
}

func (r *Registry[T]) unpin(entry *retained[T]) {
	r.mu.Lock()
	defer r.unlock()
	entry.pins--
	entry.used = r.clock()
	if entry.removed && entry.pins == 0 {
		r.disposal = append(r.disposal, entry)
	}
}

func disposeEntry[T any](entry *retained[T]) {
	if entry.dispose != nil {
		entry.dispose(entry.value)
	}
	entry.charge.Close()
}

func (r *Registry[T]) removeLocked(id string, entry *retained[T]) {
	delete(r.entries, id)
	entry.removed = true
	if entry.pins == 0 {
		r.disposal = append(r.disposal, entry)
	}
}

func (r *Registry[T]) expireLocked() {
	now := r.clock()
	for id, entry := range r.entries {
		if entry.pins == 0 && now.Sub(entry.used) >= r.ttl {
			r.removeLocked(id, entry)
		}
	}
}

func (r *Registry[T]) evictLocked() bool {
	if r.leased {
		return false
	}
	oldest := ""
	var stamp time.Time
	for id, entry := range r.entries {
		if entry.pins == 0 && (oldest == "" || entry.used.Before(stamp)) {
			oldest, stamp = id, entry.used
		}
	}
	if oldest == "" {
		return false
	}
	r.removeLocked(oldest, r.entries[oldest])
	return true
}

func (r *Registry[T]) Close() {
	r.mu.Lock()
	defer r.unlock()
	r.closed = true
	r.unregister()
	for id, entry := range r.entries {
		r.removeLocked(id, entry)
	}
}

func (r *Registry[T]) unlock() {
	disposal := r.disposal
	r.disposal = nil
	r.mu.Unlock()
	for _, entry := range disposal {
		disposeEntry(entry)
	}
}

func (r *Registry[T]) idleCandidate() idleCandidate {
	if r.leased {
		return idleCandidate{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var selected *retained[T]
	selectedID := ""
	for id, entry := range r.entries {
		if entry.pins == 0 && (selected == nil || entry.used.Before(selected.used)) {
			selected, selectedID = entry, id
		}
	}
	if selected == nil {
		return idleCandidate{}
	}
	used := selected.used
	return idleCandidate{used: used, evict: func() bool {
		r.mu.Lock()
		defer r.unlock()
		if r.entries[selectedID] != selected || selected.pins != 0 || !selected.used.Equal(used) {
			return false
		}
		r.removeLocked(selectedID, selected)
		return true
	}}
}

// Sweep releases expired idle resources even when no new requests arrive.
func (r *Registry[T]) Sweep() {
	r.mu.Lock()
	defer r.unlock()
	r.expireLocked()
}

// Invalidate removes matching resources before interrupting their work.
// Interrupt runs outside the registry lock and before disposal, including for
// idle entries.
func (r *Registry[T]) Invalidate(matches func(T) bool, interrupt func(T)) {
	r.mu.Lock()
	var removed []*retained[T]
	for id, entry := range r.entries {
		if matches(entry.value) {
			entry.pins++
			r.removeLocked(id, entry)
			removed = append(removed, entry)
		}
	}
	r.unlock()
	for _, entry := range removed {
		interrupt(entry.value)
	}
	for _, entry := range removed {
		r.unpin(entry)
	}
}

// Renew extends matching leases without acquiring their payloads.
func (r *Registry[T]) Renew(matches func(T) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock()
	for _, entry := range r.entries {
		if matches(entry.value) {
			entry.used = now
		}
	}
}

func (r *Registry[T]) ReleaseWhere(matches func(T) bool) {
	r.mu.Lock()
	defer r.unlock()
	for id, entry := range r.entries {
		if matches(entry.value) {
			r.removeLocked(id, entry)
		}
	}
}
