// Package inference coordinates physical work shared by local model runtimes.
package inference

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrQueueFull = errors.New("inference: device queue is full")
	ErrClosed    = errors.New("inference: device is closed")
	ErrWork      = errors.New("inference: invalid work identity")
)

type Priority uint8

const (
	Decision Priority = iota
	EssentialSummary
	Foreground
	Background
)

// Work identifies a bounded physical step in one runtime generation.
type Work struct {
	Owner      string
	Generation uint64
	Priority   Priority
}

// Device admits one physical step at a time. Cancellation never releases active work.
type Device struct {
	mu            sync.Mutex
	limit         int
	closed        bool
	active        *Permit
	waiters       []*ticket
	priorityHolds [4]int
}

func NewDevice(queueLimit int) (*Device, error) {
	if queueLimit < 1 {
		return nil, errors.New("inference: queue limit must be positive")
	}
	return &Device{limit: queueLimit}, nil
}

type ticket struct {
	work   Work
	ctx    context.Context
	ready  chan struct{}
	permit *Permit
	err    error
	stop   func() bool
}

// Permit remains held until the runtime has synchronized or its process has exited.
type Permit struct {
	device *Device
	work   Work
	once   sync.Once
}

func (d *Device) Acquire(ctx context.Context, work Work) (*Permit, error) {
	t, err := d.enqueue(ctx, work)
	if err != nil {
		return nil, err
	}
	<-t.ready
	if t.err != nil {
		return nil, t.err
	}
	if err := ctx.Err(); err != nil {
		// No work has been submitted by this caller yet.
		t.permit.Complete()
		return nil, err
	}
	return t.permit, nil
}

func (d *Device) enqueue(ctx context.Context, work Work) (*ticket, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.closed {
		return nil, ErrClosed
	}
	if work.Owner == "" || work.Generation == 0 || work.Priority > Background {
		return nil, ErrWork
	}
	if len(d.waiters) >= d.limit {
		return nil, ErrQueueFull
	}
	t := &ticket{work: work, ctx: ctx, ready: make(chan struct{})}
	d.waiters = append(d.waiters, t)
	t.stop = context.AfterFunc(ctx, func() { d.cancel(t) })
	d.dispatch()
	return t, nil
}

func (d *Device) cancel(t *ticket) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, waiting := range d.waiters {
		if waiting == t {
			d.remove(i)
			t.err = t.ctx.Err()
			close(t.ready)
			return
		}
	}
}

func (d *Device) remove(i int) {
	copy(d.waiters[i:], d.waiters[i+1:])
	d.waiters[len(d.waiters)-1] = nil
	d.waiters = d.waiters[:len(d.waiters)-1]
}

// dispatch runs under mu; equal priorities preserve arrival order.
func (d *Device) dispatch() {
	for d.active == nil && len(d.waiters) > 0 {
		best := 0
		for i, t := range d.waiters {
			if t.work.Priority < d.waiters[best].work.Priority {
				best = i
			}
		}
		t := d.waiters[best]
		for priority, held := range d.priorityHolds {
			if held > 0 && t.work.Priority > Priority(priority) {
				return
			}
		}
		d.remove(best)
		t.stop()
		if err := t.ctx.Err(); err != nil {
			t.err = err
			close(t.ready)
			continue
		}
		t.permit = &Permit{device: d, work: t.work}
		d.active = t.permit
		close(t.ready)
	}
}

// Complete acknowledges physical completion, including cancellation cleanup.
func (p *Permit) Complete() {
	p.once.Do(func() {
		d := p.device
		d.mu.Lock()
		defer d.mu.Unlock()
		d.active = nil
		d.dispatch()
	})
}

// Close refuses new work and cancels waiters without releasing active work.
func (d *Device) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	for _, t := range d.waiters {
		t.stop()
		t.err = ErrClosed
		close(t.ready)
	}
	d.waiters = nil
}

type Snapshot struct {
	Active *Work
	Queued int
	Closed bool
}

func (d *Device) Snapshot() Snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := Snapshot{Queued: len(d.waiters), Closed: d.closed}
	if d.active != nil {
		work := d.active.work
		s.Active = &work
	}
	return s
}
