// Package keylock serializes cancelable work on one identity without retaining idle keys.
package keylock

import (
	"context"
	"sync"
)

type entry struct {
	token chan struct{}
	users int
}

type Group struct {
	mu      sync.Mutex
	entries map[string]*entry
}

func (g *Group) Acquire(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	if g.entries == nil {
		g.entries = make(map[string]*entry)
	}
	e := g.entries[key]
	if e == nil {
		e = &entry{token: make(chan struct{}, 1)}
		g.entries[key] = e
	}
	e.users++
	g.mu.Unlock()
	select {
	case <-ctx.Done():
		g.release(key, e)
		return nil, ctx.Err()
	case e.token <- struct{}{}:
		return func() { <-e.token; g.release(key, e) }, nil
	}
}

func (g *Group) release(key string, e *entry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e.users--
	if e.users == 0 {
		delete(g.entries, key)
	}
}
