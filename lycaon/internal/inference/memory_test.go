package inference

import (
	"errors"
	"math"
	"sync"
	"testing"
)

func memoryBudget(t *testing.T, capacity uint64) *MemoryBudget {
	t.Helper()
	budget, err := NewMemoryBudget(capacity)
	if err != nil {
		t.Fatalf("new memory budget: %v", err)
	}
	t.Cleanup(budget.Close)
	return budget
}

func TestMemoryReservationBounds(t *testing.T) {
	if _, err := NewMemoryBudget(0); !errors.Is(err, ErrCapacity) {
		t.Fatalf("zero memory capacity: %v", err)
	}
	budget := memoryBudget(t, 100)
	first, err := budget.Reserve(60)
	if err != nil {
		t.Fatalf("reserve decision model: %v", err)
	}
	defer first.Release()
	for _, size := range []uint64{0, 41, math.MaxUint64} {
		if _, err := budget.Reserve(size); !errors.Is(err, ErrCapacity) {
			t.Fatalf("reservation %d: %v", size, err)
		}
	}
	second, err := budget.Reserve(40)
	if err != nil {
		t.Fatalf("reserve remaining capacity: %v", err)
	}
	defer second.Release()
	first.Release()
	first.Release()
	if got := budget.Snapshot(); got.ReservedBytes != 40 || got.CapacityBytes != 100 {
		t.Fatalf("reservation accounting: %+v", got)
	}
}

func TestSeparateComputeQueuesShareUnifiedMemory(t *testing.T) {
	cpu, gpu := device(t, 1), device(t, 1)
	budget := memoryBudget(t, 100)
	decision := submit(t, cpu, "decision", Decision)
	generation := submit(t, gpu, "generation", Foreground)
	defer decision.permit.Complete()
	defer generation.permit.Complete()
	resident, err := budget.Reserve(70)
	if err != nil {
		t.Fatalf("reserve unified memory: %v", err)
	}
	defer resident.Release()
	cpu.Close()
	if got := budget.Snapshot(); got.Closed || got.ReservedBytes != 70 {
		t.Fatalf("compute queue close changed memory ownership: %+v", got)
	}
	if _, err := budget.Reserve(31); !errors.Is(err, ErrCapacity) {
		t.Fatalf("shared memory oversubscribed: %v", err)
	}
	budget.Close()
	if got := budget.Snapshot(); !got.Closed || got.ReservedBytes != 70 {
		t.Fatalf("budget close released live memory: %+v", got)
	}
	if _, err := budget.Reserve(1); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed reservation: %v", err)
	}
	if got := gpu.Snapshot(); got.Closed || got.Active == nil {
		t.Fatalf("memory close altered GPU work: %+v", got)
	}
	resident.Release()
	if got := budget.Snapshot(); got.ReservedBytes != 0 {
		t.Fatalf("deallocated memory retained: %+v", got)
	}
}

func TestConcurrentReservationsStayWithinCapacity(t *testing.T) {
	budget := memoryBudget(t, 10)
	var workers sync.WaitGroup
	reservations := make(chan *Reservation, 40)
	for range 40 {
		workers.Go(func() {
			reservation, err := budget.Reserve(1)
			if err == nil {
				reservations <- reservation
			} else if !errors.Is(err, ErrCapacity) {
				t.Errorf("reserve concurrently: %v", err)
			}
		})
	}
	workers.Wait()
	close(reservations)
	if got := budget.Snapshot(); got.ReservedBytes != 10 || len(reservations) != 10 {
		t.Fatalf("concurrent capacity: %+v, admitted %d", got, len(reservations))
	}
	for reservation := range reservations {
		workers.Go(reservation.Release)
		workers.Go(reservation.Release)
	}
	workers.Wait()
	if got := budget.Snapshot(); got.ReservedBytes != 0 {
		t.Fatalf("concurrent release: %+v", got)
	}
}
