// Package factscope reuses repository facts across boards assembled for one prompt.
package factscope

import (
	"context"
	"sync"
)

type contextKey struct{}
type scope struct {
	mu     sync.Mutex
	values map[any]any
}

// WithScope creates a fresh boundary; no facts survive into the next preparation.
func WithScope(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKey{}, &scope{values: make(map[any]any)})
}

// Read reuses successful reads only. Keys identify the builder and source roots;
// callers treat returned repository facts as immutable.
func Read[K comparable, V any](ctx context.Context, key K, read func() (V, error)) (V, bool, error) {
	cache, _ := ctx.Value(contextKey{}).(*scope)
	if cache == nil {
		value, err := read()
		return value, false, err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if value, found := cache.values[key]; found {
		return value.(V), true, nil
	}
	value, err := read()
	if err == nil {
		cache.values[key] = value
	}
	return value, false, err
}
