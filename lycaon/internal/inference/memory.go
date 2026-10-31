package inference

import (
	"errors"
	"sync"
)

var ErrCapacity = errors.New("inference: insufficient device memory budget")

// Reservation includes resident weights, KV capacity, scratch, and retained pools.
type Reservation struct {
	device *Device
	bytes  uint64
	once   sync.Once
}

// Reserve admits memory before a runtime loads or grows its allocations.
func (d *Device) Reserve(bytes uint64) (*Reservation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, ErrClosed
	}
	if bytes == 0 || bytes > d.capacity-d.reserved {
		return nil, ErrCapacity
	}
	d.reserved += bytes
	return &Reservation{device: d, bytes: bytes}, nil
}

// Release follows confirmed deallocation or process exit.
func (r *Reservation) Release() {
	r.once.Do(func() {
		r.device.mu.Lock()
		defer r.device.mu.Unlock()
		r.device.reserved -= r.bytes
	})
}
