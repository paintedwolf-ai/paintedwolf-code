package briefing

import (
	"context"
	"sync"
)

// flightGroup shares one generation per manifest hash across targets.
type flightGroup struct {
	mu      sync.Mutex
	flights map[string]*flight
}

type flight struct {
	done   chan struct{}
	result Result
	err    error
}

func (g *flightGroup) do(ctx context.Context, key string, fn func() (Result, error)) (Result, error) {
	g.mu.Lock()
	if g.flights == nil {
		g.flights = map[string]*flight{}
	}
	if f, ok := g.flights[key]; ok {
		g.mu.Unlock()
		select {
		case <-f.done:
			return f.result, f.err
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	g.flights[key] = f
	g.mu.Unlock()
	f.result, f.err = fn()
	g.mu.Lock()
	delete(g.flights, key)
	g.mu.Unlock()
	close(f.done)
	return f.result, f.err
}
