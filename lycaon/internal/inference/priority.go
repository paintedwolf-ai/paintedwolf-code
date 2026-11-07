package inference

import "context"

// PriorityLease keeps lower-priority work behind a multi-step request.
// Physical execution still requires a separate permit for every step.
type PriorityLease struct {
	device   *Device
	priority Priority
	stop     func() bool
	released bool
}

func (d *Device) HoldPriority(ctx context.Context, work Work) (*PriorityLease, error) {
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
	total := 0
	for _, held := range d.priorityHolds {
		total += held
	}
	if total >= d.limit {
		return nil, ErrQueueFull
	}
	lease := &PriorityLease{device: d, priority: work.Priority}
	d.priorityHolds[work.Priority]++
	lease.stop = context.AfterFunc(ctx, lease.Release)
	return lease, nil
}

func (l *PriorityLease) Release() {
	d := l.device
	d.mu.Lock()
	defer d.mu.Unlock()
	if l.released {
		return
	}
	l.released = true
	l.stop()
	d.priorityHolds[l.priority]--
	d.dispatch()
}
