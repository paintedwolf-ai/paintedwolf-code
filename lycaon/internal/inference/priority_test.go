package inference

import (
	"context"
	"errors"
	"testing"
)

func TestPriorityLeaseSpansPhysicalStepsWithoutPreemption(t *testing.T) {
	d := device(t, 8)
	active := submit(t, d, "generation", Foreground)
	lease, err := d.HoldPriority(t.Context(), work("decision", Decision))
	if err != nil {
		t.Fatalf("hold decision priority: %v", err)
	}
	defer lease.Release()
	next := submit(t, d, "generation-next", Foreground)
	if d.Snapshot().Active.Owner != "generation" {
		t.Fatal("priority hold preempted physical work")
	}
	active.permit.Complete()
	for range 3 {
		if snapshot := d.Snapshot(); snapshot.Active != nil || snapshot.Queued != 1 {
			t.Fatalf("lower priority crossed a batch boundary: %+v", snapshot)
		}
		batch := submit(t, d, "decision", Decision)
		<-batch.ready
		batch.permit.Complete()
	}
	lease.Release()
	lease.Release()
	<-next.ready
	next.permit.Complete()
}

func TestPriorityCancellationPreservesActivePermit(t *testing.T) {
	d := device(t, 4)
	ctx, cancel := context.WithCancel(t.Context())
	lease, err := d.HoldPriority(ctx, work("decision", Decision))
	if err != nil {
		t.Fatalf("hold priority: %v", err)
	}
	defer lease.Release()
	active := submit(t, d, "decision", Decision)
	next := submit(t, d, "generation", Foreground)
	cancel()
	lease.Release()
	if d.Snapshot().Active.Owner != "decision" {
		t.Fatal("canceling priority released physical work")
	}
	active.permit.Complete()
	<-next.ready
	next.permit.Complete()
}

func TestCanceledPriorityUnblocksAnIdleDevice(t *testing.T) {
	d := device(t, 4)
	ctx, cancel := context.WithCancel(t.Context())
	lease, err := d.HoldPriority(ctx, work("decision", Decision))
	if err != nil {
		t.Fatalf("hold priority: %v", err)
	}
	defer lease.Release()
	next := submit(t, d, "generation", Foreground)
	cancel()
	<-next.ready
	next.permit.Complete()
}

func TestPriorityHoldsAreBoundedAndIndependent(t *testing.T) {
	d := device(t, 2)
	first, err := d.HoldPriority(t.Context(), work("first", Decision))
	if err != nil {
		t.Fatalf("first priority: %v", err)
	}
	second, err := d.HoldPriority(t.Context(), work("second", Decision))
	if err != nil {
		t.Fatalf("second priority: %v", err)
	}
	if _, err := d.HoldPriority(t.Context(), work("overflow", Decision)); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("unbounded priority holds: %v", err)
	}
	next := submit(t, d, "generation", Foreground)
	first.Release()
	if d.Snapshot().Active != nil {
		t.Fatal("one release discarded another request's priority")
	}
	second.Release()
	<-next.ready
	next.permit.Complete()
	for _, invalid := range []Work{{}, {Owner: "bad", Generation: 1, Priority: Background + 1}} {
		if _, err := d.HoldPriority(t.Context(), invalid); !errors.Is(err, ErrWork) {
			t.Fatalf("invalid priority work: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := d.HoldPriority(ctx, work("canceled", Decision)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled priority: %v", err)
	}
	d.Close()
	if _, err := d.HoldPriority(t.Context(), work("closed", Decision)); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed priority: %v", err)
	}
}
