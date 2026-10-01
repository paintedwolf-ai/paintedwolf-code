package modelcall

import (
	"testing"
	"time"
)

func TestGuardStreamStallForwardsHealthyStream(t *testing.T) {
	in := make(chan StreamChunk)
	go func() {
		defer close(in)
		for i := 0; i < 5; i++ {
			in <- StreamChunk{Content: "x"}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	stalled := false
	out := GuardStreamStall(t.Context(), in, 200*time.Millisecond, func() { stalled = true })
	var got int
	for range out {
		got++
	}
	if got != 5 {
		t.Fatalf("forwarded %d chunks want 5", got)
	}
	if stalled {
		t.Fatal("healthy stream flagged as stalled")
	}
}

func TestGuardStreamStallFiresOnSilence(t *testing.T) {
	in := make(chan StreamChunk)
	stallFired := make(chan struct{})
	go func() {
		in <- StreamChunk{Content: "x"}
		// Simulate a hung provider: close only after the stall guard fires
		// (a real provider exits when onStall cancels the stream context).
		<-stallFired
		close(in)
	}()
	out := GuardStreamStall(t.Context(), in, 30*time.Millisecond, func() { close(stallFired) })
	var got int
	for range out {
		got++
	}
	select {
	case <-stallFired:
	default:
		t.Fatal("stall guard did not fire on silent stream")
	}
	if got != 1 {
		t.Fatalf("forwarded %d chunks want 1 before stall", got)
	}
}

func TestGuardStreamStallActivityResetsWindow(t *testing.T) {
	in := make(chan StreamChunk)
	go func() {
		defer close(in)
		// Each gap is below the stall window but the total exceeds it: a slow
		// healthy emission must survive.
		for i := 0; i < 6; i++ {
			time.Sleep(20 * time.Millisecond)
			in <- StreamChunk{Content: "x"}
		}
	}()
	stalled := false
	out := GuardStreamStall(t.Context(), in, 60*time.Millisecond, func() { stalled = true })
	var got int
	for range out {
		got++
	}
	if stalled {
		t.Fatal("slow-but-steady stream flagged as stalled")
	}
	if got != 6 {
		t.Fatalf("forwarded %d chunks want 6", got)
	}
}

func TestGuardStreamStallDisabledPassthrough(t *testing.T) {
	in := make(chan StreamChunk)
	if out := GuardStreamStall(t.Context(), in, 0, nil); out != in {
		t.Fatal("stall 0 must return the input channel unchanged")
	}
}
