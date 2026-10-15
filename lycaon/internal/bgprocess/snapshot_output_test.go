package bgprocess

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
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
	h.reg.Output.SetCaptureProjector(nil)
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
	h.reg.Output.SetCaptureProjector(nil)
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

func TestTerminalPublicationAndOutputPreserveTermination(t *testing.T) {
	for _, reason := range []TerminationReason{TerminationTimedOut, TerminationStopped, TerminationExited, "", "future_reason"} {
		t.Run(string(reason), func(t *testing.T) {
			h := newProjectionHarness(t, DefaultRingBufferBytes)
			h.write("stderr", "exited successfully; timed out; stopped; permission denied")
			h.proc.running = false
			h.proc.hasExit = true
			h.proc.exitCode = 1
			h.proc.reason = reason
			h.proc.boundary = confine.Boundary{Applied: true}
			calls := 0
			h.reg.Output.complete = func(_ context.Context, c Completion) {
				calls++
				if c.TerminationReason != reason || c.Observation.TerminationReason != string(reason) {
					t.Fatalf("terminal publication lost reason: %+v", c)
				}
			}
			h.reg.Output.publishTerminal(t.Context(), h.proc)
			h.reg.Output.publishTerminal(t.Context(), h.proc)
			if calls != 1 {
				t.Fatalf("terminal publication count=%d", calls)
			}
			raw, err := h.reg.Output.ReadRawOutput("s1", "h1", 0)
			testutil.FailErr(t, "read terminal output", err)
			if raw.TerminationReason != reason || raw.Output.Running || raw.Output.ExitCode == nil || *raw.Output.ExitCode != 1 {
				t.Fatalf("output lost terminal facts: %+v", raw)
			}
		})
	}
}
