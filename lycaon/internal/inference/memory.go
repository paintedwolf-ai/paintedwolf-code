package inference

import (
	"errors"
	"math"
	"sync"
)

var ErrCapacity = errors.New("inference: insufficient memory budget")

// ReserveMemory enforces physical RAM and device working-set limits separately.
func ReserveMemory(host, device *MemoryBudget, hostBytes, deviceBytes uint64, unified bool) ([]*Reservation, error) {
	if host == nil || device == nil || host == device || hostBytes == 0 && deviceBytes == 0 {
		return nil, ErrCapacity
	}
	if unified {
		if hostBytes > math.MaxUint64-deviceBytes {
			return nil, ErrCapacity
		}
		hostBytes += deviceBytes
	}
	var reservations []*Reservation
	for _, request := range []struct {
		budget *MemoryBudget
		bytes  uint64
	}{{host, hostBytes}, {device, deviceBytes}} {
		if request.bytes == 0 {
			continue
		}
		reservation, err := request.budget.Reserve(request.bytes)
		if err != nil {
			for _, held := range reservations {
				held.Release()
			}
			return nil, err
		}
		reservations = append(reservations, reservation)
	}
	return reservations, nil
}

// MemoryBudget bounds one physical memory domain shared by CPU or GPU queues.
type MemoryBudget struct {
	mu       sync.Mutex
	capacity uint64
	reserved uint64
	closed   bool
}

func NewMemoryBudget(capacity uint64) (*MemoryBudget, error) {
	if capacity == 0 {
		return nil, ErrCapacity
	}
	return &MemoryBudget{capacity: capacity}, nil
}

type MemorySnapshot struct {
	CapacityBytes uint64
	ReservedBytes uint64
	Closed        bool
}

func (d *MemoryBudget) Snapshot() MemorySnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return MemorySnapshot{CapacityBytes: d.capacity, ReservedBytes: d.reserved, Closed: d.closed}
}

// Close refuses reservations without releasing live allocations.
func (d *MemoryBudget) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
}

// Reservation includes resident weights, KV capacity, scratch, and retained pools.
type Reservation struct {
	budget *MemoryBudget
	bytes  uint64
	once   sync.Once
}

// Reserve admits memory before a runtime loads or grows its allocations.
func (d *MemoryBudget) Reserve(bytes uint64) (*Reservation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, ErrClosed
	}
	if bytes == 0 || bytes > d.capacity-d.reserved {
		return nil, ErrCapacity
	}
	d.reserved += bytes
	return &Reservation{budget: d, bytes: bytes}, nil
}

// Release follows confirmed deallocation or process exit.
func (r *Reservation) Release() {
	r.once.Do(func() {
		r.budget.mu.Lock()
		defer r.budget.mu.Unlock()
		r.budget.reserved -= r.bytes
	})
}
