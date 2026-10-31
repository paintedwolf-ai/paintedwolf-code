package inference

import (
	"context"
	"errors"
	"math"
	"testing"
)

func device(t *testing.T, queue int) *Device {
	t.Helper()
	d, err := NewDevice(queue, 100)
	if err != nil {
		t.Fatalf("new device: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

func work(name string, priority Priority) Work {
	return Work{Owner: name, Generation: 1, Priority: priority}
}

func submit(t *testing.T, d *Device, name string, priority Priority) *ticket {
	t.Helper()
	q, err := d.enqueue(context.Background(), work(name, priority))
	if err != nil {
		t.Fatalf("submit %s: %v", name, err)
	}
	return q
}

func TestDevicePrioritizesDecisionsAtPhysicalBoundaries(t *testing.T) {
	d := device(t, 8)
	active := submit(t, d, "generation", Foreground)
	background := submit(t, d, "background", Background)
	foreground := submit(t, d, "foreground", Foreground)
	summary := submit(t, d, "summary", EssentialSummary)
	first := submit(t, d, "decision-1", Decision)
	second := submit(t, d, "decision-2", Decision)
	if got := d.Snapshot(); got.Active.Owner != "generation" || got.Queued != 5 {
		t.Fatalf("active step was preempted: %+v", got)
	}
	active.permit.Complete()
	for _, next := range []*ticket{first, second, summary, foreground, background} {
		<-next.ready
		if got := d.Snapshot(); got.Active.Owner != next.work.Owner {
			t.Fatalf("admitted %v, want %v", got.Active, next.work)
		}
		next.permit.Complete()
		active.permit.Complete()
	}
	if got := d.Snapshot(); got.Active != nil || got.Queued != 0 {
		t.Fatalf("device did not drain: %+v", got)
	}
}

func TestCancellationDoesNotReleasePhysicalWork(t *testing.T) {
	d := device(t, 2)
	ctx, cancel := context.WithCancel(context.Background())
	permit, err := d.Acquire(ctx, work("generation", Foreground))
	if err != nil {
		t.Fatalf("acquire generation: %v", err)
	}
	defer permit.Complete()
	next := submit(t, d, "decision", Decision)
	cancel()
	if got := d.Snapshot(); got.Active.Owner != "generation" || got.Queued != 1 {
		t.Fatalf("cancellation released an unsynchronized step: %+v", got)
	}
	permit.Complete()
	<-next.ready
	next.permit.Complete()
}

func TestCanceledWaiterReclaimsQueueCapacity(t *testing.T) {
	d := device(t, 1)
	active := submit(t, d, "active", Foreground)
	defer active.permit.Complete()
	ctx, cancel := context.WithCancel(context.Background())
	waiting, err := d.enqueue(ctx, work("waiting", Foreground))
	if err != nil {
		t.Fatalf("enqueue waiter: %v", err)
	}
	if _, err := d.enqueue(context.Background(), work("overflow", Decision)); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("full queue: %v", err)
	}
	cancel()
	<-waiting.ready
	if !errors.Is(waiting.err, context.Canceled) {
		t.Fatalf("canceled waiter: %v", waiting.err)
	}
	next := submit(t, d, "replacement", Decision)
	active.permit.Complete()
	<-next.ready
	next.permit.Complete()
}

func TestClosePreservesActiveWorkAndMemory(t *testing.T) {
	d := device(t, 1)
	active := submit(t, d, "active", Foreground)
	waiting := submit(t, d, "waiting", Decision)
	reservation, err := d.Reserve(90)
	if err != nil {
		t.Fatalf("reserve resident model: %v", err)
	}
	d.Close()
	<-waiting.ready
	if !errors.Is(waiting.err, ErrClosed) {
		t.Fatalf("closed waiter: %v", waiting.err)
	}
	if got := d.Snapshot(); got.Active == nil || got.ReservedBytes != 90 || !got.Closed {
		t.Fatalf("close released physical resources: %+v", got)
	}
	if _, err := d.Acquire(context.Background(), work("late", Decision)); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed acquisition: %v", err)
	}
	if _, err := d.Reserve(1); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed reservation: %v", err)
	}
	active.permit.Complete()
	reservation.Release()
	reservation.Release()
	if got := d.Snapshot(); got.Active != nil || got.ReservedBytes != 0 {
		t.Fatalf("completed resources retained: %+v", got)
	}
}

func TestMemoryReservationBounds(t *testing.T) {
	d := device(t, 1)
	first, err := d.Reserve(60)
	if err != nil {
		t.Fatalf("reserve decision model: %v", err)
	}
	defer first.Release()
	for _, size := range []uint64{0, 41, math.MaxUint64} {
		if _, err := d.Reserve(size); !errors.Is(err, ErrCapacity) {
			t.Fatalf("reservation %d: %v", size, err)
		}
	}
	second, err := d.Reserve(40)
	if err != nil {
		t.Fatalf("reserve remaining capacity: %v", err)
	}
	defer second.Release()
	first.Release()
	first.Release()
	if got := d.Snapshot(); got.ReservedBytes != 40 {
		t.Fatalf("reservation accounting: %+v", got)
	}
}

func TestAdmissionRejectsInvalidWork(t *testing.T) {
	for _, limits := range []struct {
		queue  int
		memory uint64
	}{{0, 1}, {-1, 1}, {1, 0}} {
		if _, err := NewDevice(limits.queue, limits.memory); err == nil {
			t.Fatalf("accepted limits: %+v", limits)
		}
	}
	d := device(t, 1)
	for _, w := range []Work{{}, {Owner: "x"}, {Generation: 1}, {Owner: "x", Generation: 1, Priority: 255}} {
		if _, err := d.Acquire(context.Background(), w); !errors.Is(err, ErrWork) {
			t.Fatalf("accepted work %+v: %v", w, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Acquire(ctx, work("canceled", Decision)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission: %v", err)
	}
}
