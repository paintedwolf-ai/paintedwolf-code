// Package workscope binds work to an owner that stops admission, cancels, and drains.
package workscope

import (
	"context"
	"sync"
)

// Group's zero value admits work until Stop. It must not be copied after use.
type Group struct {
	mu      sync.Mutex
	stopped bool
	next    uint64
	active  map[uint64]context.CancelFunc
	idle    chan struct{}
}

// Begin registers synchronous or detached work before it starts.
func (g *Group) Begin(parent context.Context) (context.Context, func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return nil, nil, context.Canceled
	}
	ctx, cancel := context.WithCancel(parent)
	if len(g.active) == 0 {
		g.active = make(map[uint64]context.CancelFunc)
		g.idle = make(chan struct{})
	}
	g.next++
	id := g.next
	g.active[id] = cancel
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			cancel()
			g.mu.Lock()
			defer g.mu.Unlock()
			delete(g.active, id)
			if len(g.active) == 0 {
				close(g.idle)
			}
		})
	}, nil
}

// Stop seals admission before cancelling every registered operation.
func (g *Group) Stop() {
	g.mu.Lock()
	g.stopped = true
	cancels := make([]context.CancelFunc, 0, len(g.active))
	for _, cancel := range g.active {
		cancels = append(cancels, cancel)
	}
	g.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// Wait drains the current busy interval. Stop first for a final owner drain.
func (g *Group) Wait(ctx context.Context) error {
	g.mu.Lock()
	idle := g.idle
	empty := len(g.active) == 0
	g.mu.Unlock()
	if empty {
		return nil
	}
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
