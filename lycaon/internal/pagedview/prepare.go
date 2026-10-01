package pagedview

import (
	"context"
	"sync"
)

type flight[T any] struct {
	ctx                 context.Context
	cancel              context.CancelFunc
	done, changed       chan struct{}
	interests           int
	serial              uint64
	demands             map[uint64]int
	result              T
	err                 error
	published, finished bool
}

// Preparation shares work, while each caller can cancel only its waiting interest.
// The adapter includes authorization scope and immutable source identity in K.
type Preparation[K comparable, T any] struct {
	mu      sync.Mutex
	pending map[K]*flight[T]
	live    map[*flight[T]]struct{}
	closed  bool
}

func (p *Preparation[K, T]) Do(ctx context.Context, key K, prepare func(context.Context) (T, error)) (T, error) {
	return p.Join(ctx, key, 0, nil, func(ctx context.Context, _ func(T), _ func() int) (T, error) { return prepare(ctx) })
}

// Join reads immutable publications; preparation stops when its last interest leaves.
func (p *Preparation[K, T]) Join(ctx context.Context, key K, limit int, measure func(T) int, prepare func(context.Context, func(T), func() int) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return zero, ErrExpired
	}
	if p.pending == nil {
		p.pending = make(map[K]*flight[T])
		p.live = make(map[*flight[T]]struct{})
	}
	f := p.pending[key]
	if f == nil {
		work, cancel := context.WithCancel(context.WithoutCancel(ctx)) // #nosec G118 -- The shared flight cancels on completion, its last interest, or shutdown.
		f = &flight[T]{ctx: work, cancel: cancel, done: make(chan struct{}), changed: make(chan struct{})}
		p.pending[key] = f
		p.live[f] = struct{}{}
		go p.run(key, f, prepare)
	}
	f.interests++
	f.serial++
	interest := f.serial
	if f.demands == nil {
		f.demands = make(map[uint64]int)
	}
	f.demands[interest] = limit
	p.mu.Unlock()
	defer p.leave(key, f, interest)
	for {
		p.mu.Lock()
		result, err, published, finished, changed := f.result, f.err, f.published, f.finished, f.changed
		p.mu.Unlock()
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if finished {
			return result, err
		}
		if published && limit > 0 && measure != nil && measure(result) >= limit {
			return result, nil
		}
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-changed:
		}
	}
}

func (p *Preparation[K, T]) run(key K, f *flight[T], prepare func(context.Context, func(T), func() int) (T, error)) {
	result, err := prepare(f.ctx, func(value T) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if f.ctx.Err() != nil {
			return
		}
		f.result, f.published = value, true
		close(f.changed)
		f.changed = make(chan struct{})
	}, func() int {
		p.mu.Lock()
		defer p.mu.Unlock()
		if len(f.demands) < f.interests {
			return 0
		}
		maximum := 0
		for _, limit := range f.demands {
			if limit == 0 {
				return 0
			}
			maximum = max(maximum, limit)
		}
		return maximum
	})
	p.mu.Lock()
	f.result, f.err, f.finished = result, err, true
	if p.pending[key] == f {
		delete(p.pending, key)
	}
	close(f.changed)
	close(f.done)
	delete(p.live, f)
	f.cancel()
	p.mu.Unlock()
}
func (p *Preparation[K, T]) leave(key K, f *flight[T], interest uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f.interests--
	delete(f.demands, interest)
	if f.interests == 0 {
		if p.pending[key] == f {
			delete(p.pending, key)
		}
		f.cancel()
	}
}

// CancelAll returns completion signals for waiting outside domain locks.
func (p *Preparation[K, T]) CancelAll() []<-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	pending := make([]<-chan struct{}, 0, len(p.live))
	for f := range p.live {
		f.cancel()
		pending = append(pending, f.done)
	}
	clear(p.pending)
	return pending
}

// Close stops admission and waits for every preparation to release its resources.
func (p *Preparation[K, T]) Close() {
	p.mu.Lock()
	p.closed = true
	pending := make([]<-chan struct{}, 0, len(p.live))
	for f := range p.live {
		f.cancel()
		pending = append(pending, f.done)
	}
	p.mu.Unlock()
	for _, done := range pending {
		<-done
	}
}
