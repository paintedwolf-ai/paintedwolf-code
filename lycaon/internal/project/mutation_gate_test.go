package project

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMutationGateSerializesOneProjectOnly(t *testing.T) {
	gate := NewMutationGate()
	if err := gate.BeginMutation("p1"); err != nil {
		t.Fatalf("begin p1: %v", err)
	}
	if err := gate.BeginMutation("p1"); !errors.Is(err, ErrMutationInProgress) {
		t.Fatalf("second begin = %v", err)
	}
	if _, err := gate.BeginRuntime("p1"); !errors.Is(err, ErrMutationInProgress) {
		t.Fatalf("runtime during mutation = %v", err)
	}
	if err := gate.BeginMutation("p2"); err != nil {
		t.Fatalf("begin p2: %v", err)
	}
	gate.EndMutation("p1")
	releaseRuntime, err := gate.BeginRuntime("p1")
	if err != nil {
		t.Fatalf("begin runtime p1: %v", err)
	}
	if err := gate.BeginMutation("p1"); !errors.Is(err, ErrProjectBusy) {
		t.Fatalf("mutation during runtime = %v", err)
	}
	releaseRuntime()
	releaseRuntime()
	if err := gate.BeginMutation("p1"); err != nil {
		t.Fatalf("begin after runtime release: %v", err)
	}
}

func TestMutationGateDrainsExistingRuntimeAndRejectsNewWork(t *testing.T) {
	gate := NewMutationGate()
	releaseRuntime, err := gate.BeginRuntime("p1")
	if err != nil {
		t.Fatalf("begin runtime: %v", err)
	}
	wait, err := gate.BeginDrainingMutation("p1")
	if err != nil {
		t.Fatalf("begin drain: %v", err)
	}
	if _, err := gate.BeginRuntime("p1"); !errors.Is(err, ErrMutationInProgress) {
		t.Fatalf("runtime admitted during drain: %v", err)
	}
	if gate.MutationDrained("p1") {
		t.Fatal("mutation reported drained while runtime is active")
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait before release = %v", err)
	}
	releaseRuntime()
	if !gate.MutationDrained("p1") {
		t.Fatal("mutation did not report drained after runtime release")
	}
	if err := wait(context.Background()); err != nil {
		t.Fatalf("wait after release: %v", err)
	}
	gate.EndMutation("p1")
	if _, err := gate.BeginRuntime("p1"); err != nil {
		t.Fatalf("runtime after mutation: %v", err)
	}
}
