package testutil

import (
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// fatalRecorder ends the calling goroutine on Fatalf and keeps the message.
type fatalRecorder struct {
	testing.TB
	message string
}

func (r *fatalRecorder) Helper() {}

func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.message = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// runProgressWait runs the wait and returns its failure message, or "" when it returned.
func runProgressWait(t *testing.T, step string, cond func() bool, progress func() Progress) string {
	recorder := &fatalRecorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		WaitForProgress(recorder, step, cond, progress)
	}()
	AwaitClosed(t, "the progress wait ends", done)
	return recorder.message
}

func TestWaitForProgressFailsOnceNothingCanMakeTheConditionHold(t *testing.T) {
	var conds atomic.Int32
	message := runProgressWait(t, "the reply arrives", func() bool {
		conds.Add(1)
		return false
	}, func() Progress {
		return Progress{Epoch: 4, Idle: true, Report: "wake parked: not actionable"}
	})
	if !strings.HasPrefix(message, "the reply arrives: ") || !strings.Contains(message, "wake parked: not actionable") {
		t.Fatalf("failure = %q, want the step and the report", message)
	}
	if got := conds.Load(); got != 1 {
		t.Fatalf("condition read %d times, want 1: an idle system fails on the first false reading", got)
	}
}

func TestWaitForProgressWaitsWhileWorkRemains(t *testing.T) {
	var polls atomic.Int32
	message := runProgressWait(t, "the reply arrives", func() bool {
		return polls.Add(1) == 3
	}, func() Progress {
		return Progress{Epoch: uint64(polls.Load()), Idle: false}
	})
	if message != "" {
		t.Fatalf("busy wait failed: %q", message)
	}
}

// Work that starts and ends around the condition reading leaves the system
// idle on both sides; the changed epoch makes the wait read the condition again.
func TestWaitForProgressRereadsWhenWorkRanAroundTheCondition(t *testing.T) {
	var reads, epoch atomic.Uint64
	message := runProgressWait(t, "the reply arrives", func() bool {
		if reads.Add(1) == 1 {
			epoch.Add(1)
			return false
		}
		return true
	}, func() Progress {
		return Progress{Epoch: epoch.Load(), Idle: true}
	})
	if message != "" {
		t.Fatalf("wait failed although work ran while the condition was read: %q", message)
	}
	if got := reads.Load(); got != 2 {
		t.Fatalf("condition read %d times, want 2", got)
	}
}
