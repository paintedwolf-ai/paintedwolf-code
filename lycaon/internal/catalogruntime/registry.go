package catalogruntime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Registry is a concurrency-safe stable-id index for domain adapters.
type Registry[T any] struct {
	mu   sync.RWMutex
	byID map[string]T
}

// NewRegistry constructs an empty adapter registry.
func NewRegistry[T any]() *Registry[T] {
	return &Registry[T]{byID: make(map[string]T)}
}

// Register adds or replaces one adapter under id.
func (r *Registry[T]) Register(id string, value T) error {
	if r == nil {
		return fmt.Errorf("catalog runtime: nil registry")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("catalog runtime: adapter id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byID == nil {
		r.byID = make(map[string]T)
	}
	r.byID[id] = value
	return nil
}

// Add registers one adapter and rejects a duplicate stable id.
func (r *Registry[T]) Add(id string, value T) error {
	if r == nil {
		return fmt.Errorf("catalog runtime: nil registry")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("catalog runtime: adapter id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byID == nil {
		r.byID = make(map[string]T)
	}
	if _, exists := r.byID[id]; exists {
		return fmt.Errorf("catalog runtime: adapter %q already registered", id)
	}
	r.byID[id] = value
	return nil
}

// Replace atomically installs a complete registry generation.
func (r *Registry[T]) Replace(values map[string]T) {
	if r == nil {
		return
	}
	next := make(map[string]T, len(values))
	for id, value := range values {
		next[id] = value
	}
	r.mu.Lock()
	r.byID = next
	r.mu.Unlock()
}

// Get resolves one adapter.
func (r *Registry[T]) Get(id string) (T, bool) {
	var zero T
	if r == nil {
		return zero, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.byID[id]
	return value, ok
}

// IDs returns registered ids in stable order.
func (r *Registry[T]) IDs() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.byID))
	for id := range r.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Factory constructs an adapter from a resolved domain specification.
type Factory[S, A any] func(context.Context, S) (A, error)

// FactorySet selects explicit kind factories with an optional declared fallback.
type FactorySet[S, A any] struct {
	byKind   map[string]Factory[S, A]
	fallback Factory[S, A]
}

// NewFactorySet copies factories into an immutable selector.
func NewFactorySet[S, A any](byKind map[string]Factory[S, A], fallback Factory[S, A]) FactorySet[S, A] {
	copyByKind := make(map[string]Factory[S, A], len(byKind))
	for kind, factory := range byKind {
		copyByKind[kind] = factory
	}
	return FactorySet[S, A]{byKind: copyByKind, fallback: fallback}
}

// Build constructs an adapter using the exact kind or the declared fallback.
func (f FactorySet[S, A]) Build(ctx context.Context, kind string, spec S) (A, error) {
	if factory, ok := f.byKind[kind]; ok {
		return factory(ctx, spec)
	}
	if f.fallback != nil {
		return f.fallback(ctx, spec)
	}
	var zero A
	return zero, fmt.Errorf("catalog runtime: no factory for kind %q", kind)
}

// Kinds returns the explicitly registered kinds in stable order. Declared
// fallbacks are absent because they do not enumerate kinds.
func (f FactorySet[S, A]) Kinds() []string {
	kinds := make([]string, 0, len(f.byKind))
	for kind := range f.byKind {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}
