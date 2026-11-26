package testutil

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
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

// progressScript drives one wait: each condition read advances the clock by
// step and returns the scripted reading for that poll.
type progressScript struct {
	now      time.Time
	step     time.Duration
	reads    int
	holdsAt  int
	idle     func(read int) bool
	epochFor func(read int) uint64
}

func (s *progressScript) clock() time.Time { return s.now }

func (s *progressScript) cond() bool {
	s.reads++
	s.now = s.now.Add(s.step)
	return s.holdsAt > 0 && s.reads >= s.holdsAt
}

func (s *progressScript) progress() Progress {
	return Progress{Epoch: s.epochFor(s.reads), Idle: s.idle(s.reads), Report: "wake parked: not actionable"}
}

// run waits on the script and returns the failure message, or "" when the wait returned.
func (s *progressScript) run(t *testing.T) string {
	recorder := &fatalRecorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		waitForProgress(recorder, "the reply arrives", s.cond, s.progress, s.clock)
	}()
	AwaitClosed(t, "the progress wait ends", done)
	return recorder.message
}

func newScript(holdsAt int, idle func(int) bool, epoch func(int) uint64) *progressScript {
	return &progressScript{now: time.Unix(0, 0), step: ProgressIdleWindow/2 + time.Second, holdsAt: holdsAt, idle: idle, epochFor: epoch}
}

func always(idle bool) func(int) bool      { return func(int) bool { return idle } }
func fixedEpoch(e uint64) func(int) uint64 { return func(int) uint64 { return e } }

func TestWaitForProgressFailsAfterIdleLastsTheWholeWindow(t *testing.T) {
	s := newScript(0, always(true), fixedEpoch(4))
	message := s.run(t)
	if !strings.HasPrefix(message, "the reply arrives: ") || !strings.Contains(message, ProgressIdleWindow.String()) ||
		!strings.Contains(message, "wake parked: not actionable") {
		t.Fatalf("failure = %q, want the step, the window and the report", message)
	}
	// The window opens at the first idle reading and is crossed two readings later.
	if s.reads != 3 {
		t.Fatalf("condition read %d times, want 3", s.reads)
	}
}

func TestWaitForProgressKeepsWaitingWhenIdleBreaksBeforeTheWindow(t *testing.T) {
	// Idle for two readings, busy on the third, idle again until the condition holds on the sixth.
	s := newScript(6, func(read int) bool { return read != 3 }, fixedEpoch(4))
	if message := s.run(t); message != "" {
		t.Fatalf("wait failed although idle never lasted the window: %q", message)
	}
}

func TestWaitForProgressRestartsTheWindowWhenWorkRuns(t *testing.T) {
	// Work runs around every condition read, so each reading sees a new epoch.
	s := newScript(6, always(true), func(read int) uint64 { return uint64(read) })
	if message := s.run(t); message != "" {
		t.Fatalf("wait failed although work kept running: %q", message)
	}
}

func TestWaitForProgressReturnsOnceTheConditionHolds(t *testing.T) {
	s := newScript(2, always(false), fixedEpoch(1))
	if message := s.run(t); message != "" {
		t.Fatalf("busy wait failed: %q", message)
	}
	if s.reads != 2 {
		t.Fatalf("condition read %d times, want 2", s.reads)
	}
}
