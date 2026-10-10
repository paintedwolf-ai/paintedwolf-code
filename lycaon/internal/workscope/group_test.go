package workscope

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStopCancelsAndWaitJoinsWork(t *testing.T) {
	var group Group
	ctx, finish, err := group.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin work: %v", err)
	}
	group.Stop()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("work context survived owner stop")
	}
	if _, _, err := group.Begin(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("admission after stop: %v", err)
	}
	deadline, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := group.Wait(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait before completion: %v", err)
	}
	finish()
	finish()
	if err := group.Wait(t.Context()); err != nil {
		t.Fatalf("drain completed work: %v", err)
	}
}

func TestWaitTracksEachBusyInterval(t *testing.T) {
	var group Group
	for range 3 {
		_, finish, err := group.Begin(t.Context())
		if err != nil {
			t.Fatalf("begin work: %v", err)
		}
		finish()
		if err := group.Wait(t.Context()); err != nil {
			t.Fatalf("wait interval: %v", err)
		}
	}
}

func TestSealPreservesWorkUntilShutdown(t *testing.T) {
	var group Group
	ctx, finish, err := group.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	group.Seal()
	if ctx.Err() != nil {
		t.Fatal("seal canceled admitted work")
	}
	if _, _, err := group.Begin(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("sealed admission: %v", err)
	}
	group.Stop()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("shutdown did not cancel sealed work")
	}
}
