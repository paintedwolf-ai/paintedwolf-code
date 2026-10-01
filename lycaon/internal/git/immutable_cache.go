package git

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
)

const immutableCacheBytes = 32 << 20
const immutableEntryBytes = 4 << 20

type immutableEntry[T any] struct {
	key   string
	value T
	bytes int
}

// Resolved object keys share immutable values; callers copy projections.
type immutableCache[T any] struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   list.List
	bytes   int
	flights map[string]*immutableFlight[T]
}

func (c *immutableCache[T]) getLocked(key string) (T, bool) {
	if item := c.entries[key]; item != nil {
		c.order.MoveToFront(item)
		return item.Value.(immutableEntry[T]).value, true
	}
	var zero T
	return zero, false
}

func (c *immutableCache[T]) putLocked(key string, value T, bytes int) {
	if bytes <= 0 || bytes > immutableEntryBytes {
		return
	}
	if c.entries == nil {
		c.entries = make(map[string]*list.Element)
	}
	if item := c.entries[key]; item != nil {
		c.bytes -= item.Value.(immutableEntry[T]).bytes
		c.order.Remove(item)
	}
	c.entries[key] = c.order.PushFront(immutableEntry[T]{key, value, bytes})
	c.bytes += bytes
	for c.bytes > immutableCacheBytes || len(c.entries) > 128 {
		last := c.order.Back()
		entry := last.Value.(immutableEntry[T])
		delete(c.entries, entry.key)
		c.bytes -= entry.bytes
		c.order.Remove(last)
	}
}

type immutableFlight[T any] struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	value   T
	err     error
}

func (c *immutableCache[T]) load(ctx context.Context, key string, read func(context.Context) (T, int, error)) (T, string, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, "canceled", err
	}
	c.mu.Lock()
	if value, ok := c.getLocked(key); ok {
		c.mu.Unlock()
		return value, "hit", nil
	}
	flight := c.flights[key]
	disposition := "shared"
	if flight == nil {
		disposition = "miss"
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second) // #nosec G118 -- Completion or the last reader cancels acquisition.
		flight = &immutableFlight[T]{done: make(chan struct{}), cancel: cancel}
		if c.flights == nil {
			c.flights = make(map[string]*immutableFlight[T])
		}
		c.flights[key] = flight
		go c.acquire(work, key, flight, read)
	}
	flight.waiters++
	c.mu.Unlock()
	defer c.release(key, flight)
	select {
	case <-ctx.Done():
		return zero, disposition, ctx.Err()
	case <-flight.done:
		if err := ctx.Err(); err != nil {
			return zero, disposition, err
		}
		if flight.err != nil {
			return zero, disposition, flight.err
		}
		return flight.value, disposition, nil
	}
}

func (c *immutableCache[T]) acquire(ctx context.Context, key string, flight *immutableFlight[T], read func(context.Context) (T, int, error)) {
	defer flight.cancel()
	value, bytes, err := acquireImmutable(ctx, read)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		err = ctx.Err()
	}
	flight.value, flight.err = value, err
	if c.flights[key] == flight {
		delete(c.flights, key)
		if err == nil {
			c.putLocked(key, value, bytes)
		}
	}
	close(flight.done)
}

func (c *immutableCache[T]) release(key string, flight *immutableFlight[T]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	flight.waiters--
	if flight.waiters == 0 && c.flights[key] == flight {
		delete(c.flights, key)
		flight.cancel()
	}
}

func acquireImmutable[T any](ctx context.Context, read func(context.Context) (T, int, error)) (value T, bytes int, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			observability.LogRecoveredPanic("git.immutable_cache.acquire", recovered)
			err = fmt.Errorf("acquiring the Git cache failed")
		}
	}()
	return read(ctx)
}
