package bgprocess

import (
	"context"
	"strings"
	"testing"
)

// A foreground snapshot keeps the whole screened body next to the cut tail,
// so a caller can spill what the tail dropped instead of losing it at Discard.
func TestSnapshotKeepsScreenedOutputBeyondTheTail(t *testing.T) {
	h := newProjectionHarness(t, DefaultRingBufferBytes)
	h.write("stdout", strings.Repeat("a", 200))
	h.write("stdout", "capture-secret-value tail-end")

	snap, err := h.reg.Snapshot(context.Background(), "s1", "h1", 32)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !snap.OutputScreened {
		t.Fatal("output screened by a wired projector must say so")
	}
	if snap.OutputEvicted {
		t.Fatal("nothing was evicted from a buffer this small")
	}
	if len(snap.Tail) > 32 || !strings.HasSuffix(snap.Output, snap.Tail) {
		t.Fatalf("tail %q is not the cut suffix of output (%d bytes)", snap.Tail, len(snap.Output))
	}
	if !strings.Contains(snap.Output, strings.Repeat("a", 200)) {
		t.Fatal("output lost the bytes the tail dropped")
	}
	if strings.Contains(snap.Output, "secret-value") || !strings.Contains(snap.Output, "[REDACTED]") {
		t.Fatalf("output left the screen unapplied: %q", snap.Output)
	}
}

func TestSnapshotOutputUnscreenedWithoutProjector(t *testing.T) {
	h := newProjectionHarness(t, DefaultRingBufferBytes)
	h.reg.SetCaptureProjector(nil)
	h.write("stdout", "plain output")

	snap, err := h.reg.Snapshot(context.Background(), "s1", "h1", 64)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.OutputScreened {
		t.Fatal("an unwired projector cannot vouch for the output")
	}
	if snap.Output != snap.Tail || !strings.Contains(snap.Output, "plain output") {
		t.Fatalf("output/tail = %q / %q", snap.Output, snap.Tail)
	}
}

func TestSnapshotReportsRingEviction(t *testing.T) {
	h := newProjectionHarness(t, 64)
	h.reg.SetCaptureProjector(nil)
	// Eviction happens on the append after the buffer overflows.
	h.write("stdout", strings.Repeat("b", 60))
	h.write("stdout", strings.Repeat("c", 60))
	h.write("stdout", strings.Repeat("d", 60))

	snap, err := h.reg.Snapshot(context.Background(), "s1", "h1", 0)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !snap.OutputEvicted {
		t.Fatal("an overflowed ring must report that output is a suffix")
	}
}
