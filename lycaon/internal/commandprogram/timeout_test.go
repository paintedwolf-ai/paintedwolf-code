package commandprogram

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// manualClock schedules timers that fire only when the test fires them; each
// scheduling is an observed event on scheduled.
type manualClock struct {
	mu        sync.Mutex
	now       time.Time
	timers    []*manualTimer
	scheduled chan time.Duration
}

type manualTimer struct {
	after   time.Duration
	fn      func()
	stopped bool
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Unix(1_000_000, 0), scheduled: make(chan time.Duration, 8)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) AfterFunc(d time.Duration, fn func()) func() bool {
	timer := &manualTimer{after: d, fn: fn}
	c.mu.Lock()
	c.timers = append(c.timers, timer)
	c.mu.Unlock()
	c.scheduled <- d
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !timer.stopped
		timer.stopped = true
		return was
	}
}

// advance moves the clock by d without firing anything.
func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// fireNext waits for the next scheduled timer, checks its interval and runs it.
func (c *manualClock) fireNext(t *testing.T, want time.Duration) {
	t.Helper()
	if got := <-c.scheduled; got != want {
		t.Fatalf("scheduled %v, want %v", got, want)
	}
	c.mu.Lock()
	timer := c.timers[len(c.timers)-1]
	c.now = c.now.Add(timer.after)
	stopped := timer.stopped
	c.mu.Unlock()
	if !stopped {
		timer.fn()
	}
}

type timeoutRun struct {
	result toolcontract.ProducerResult
	err    error
}

func startTimeout(clock Clock, plan TimeoutPlan, sent func(Signal), run func(context.Context, *SignalRelay) (toolcontract.ProducerResult, error)) <-chan timeoutRun {
	done := make(chan timeoutRun, 1)
	go func() {
		result, err := RunTimeout(context.Background(), clock, plan, sent, run)
		done <- timeoutRun{result, err}
	}()
	return done
}

var term = Signal{Name: "TERM", Number: 15}

// nativeInner has no process to signal; it ends only through its context.
func nativeInner(ctx context.Context, _ *SignalRelay) (toolcontract.ProducerResult, error) {
	<-ctx.Done()
	return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.NoEffect}, ctx.Err()
}

func TestRunTimeoutInterruptsANativeCommandOnExpiry(t *testing.T) {
	for _, tc := range []struct {
		name     string
		preserve bool
		want     int
	}{{"timed out status", false, TimedOutStatus}, {"preserved signal status", true, 128 + 15}} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newManualClock()
			done := startTimeout(clock, TimeoutPlan{Duration: time.Second, Signal: term, PreserveStatus: tc.preserve}, nil, nativeInner)
			clock.fireNext(t, time.Second)
			got := <-done
			if got.err != nil || got.result.Status != tc.want || got.result.Terminal != toolcontract.ProducerCompleted || got.result.Validate() != nil {
				t.Fatalf("result = %+v, %v; want completed status %d", got.result, got.err, tc.want)
			}
		})
	}
}

// processInner stands for an external process that ignores TERM: it records
// every relayed signal and exits only on KILL, with the shell's 137.
func processInner(received chan<- Signal) func(context.Context, *SignalRelay) (toolcontract.ProducerResult, error) {
	return func(ctx context.Context, relay *SignalRelay) (toolcontract.ProducerResult, error) {
		killed := make(chan struct{})
		var once sync.Once
		detach := relay.Attach(func(sig Signal) bool {
			received <- sig
			if sig == Kill {
				once.Do(func() { close(killed) })
			}
			return true
		})
		defer detach()
		select {
		case <-killed:
			return toolcontract.ProducerResult{Status: 137, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectAttempted}, nil
		case <-ctx.Done():
			return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.EffectUnknown}, ctx.Err()
		}
	}
}

func TestRunTimeoutEscalatesToKillAfterTheSignalIsIgnored(t *testing.T) {
	for _, tc := range []struct {
		name     string
		preserve bool
		want     int
	}{{"timed out status", false, TimedOutStatus}, {"preserved kill status", true, 137}} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newManualClock()
			received := make(chan Signal, 4)
			var sent []string
			var mu sync.Mutex
			done := startTimeout(clock, TimeoutPlan{Duration: time.Second, KillAfter: 2 * time.Second, Signal: term, PreserveStatus: tc.preserve}, func(sig Signal) {
				mu.Lock()
				sent = append(sent, sig.Name)
				mu.Unlock()
			}, processInner(received))
			clock.fireNext(t, time.Second)
			if sig := <-received; sig != term {
				t.Fatalf("first signal = %v, want TERM", sig)
			}
			clock.fireNext(t, 2*time.Second)
			if sig := <-received; sig != Kill {
				t.Fatalf("escalation = %v, want KILL", sig)
			}
			got := <-done
			if got.err != nil || got.result.Status != tc.want || got.result.EffectCertainty != toolcontract.EffectAttempted {
				t.Fatalf("result = %+v, %v; want status %d with the process's effect certainty", got.result, got.err, tc.want)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(sent) != 2 || sent[0] != "TERM" || sent[1] != "KILL" {
				t.Fatalf("observed deliveries %v, want TERM then KILL", sent)
			}
		})
	}
}

func TestRunTimeoutPassesThroughACommandThatFinishes(t *testing.T) {
	clock := newManualClock()
	finished := toolcontract.ProducerResult{Status: 3, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectAttempted}
	result, err := RunTimeout(context.Background(), clock, TimeoutPlan{Duration: time.Minute, KillAfter: time.Second, Signal: term}, nil, func(context.Context, *SignalRelay) (toolcontract.ProducerResult, error) {
		return finished, nil
	})
	if err != nil || result != finished {
		t.Fatalf("result = %+v, %v; want the command's own %+v", result, err, finished)
	}
	if got := <-clock.scheduled; got != time.Minute {
		t.Fatalf("scheduled %v, want the duration only", got)
	}
	if len(clock.scheduled) != 0 || !clock.timers[0].stopped {
		t.Fatalf("expiry timer still armed after the command finished: %+v", clock.timers)
	}
}

func TestRunTimeoutZeroDurationDisablesTheTimer(t *testing.T) {
	clock := newManualClock()
	result, err := RunTimeout(context.Background(), clock, TimeoutPlan{Signal: term}, nil, func(context.Context, *SignalRelay) (toolcontract.ProducerResult, error) {
		return ordinaryResult(0), nil
	})
	if err != nil || result.Status != 0 || len(clock.timers) != 0 {
		t.Fatalf("result = %+v, %v with %d timers; want a plain run", result, err, len(clock.timers))
	}
}

func TestRunTimeoutOuterCancellationWins(t *testing.T) {
	clock := newManualClock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan timeoutRun, 1)
	go func() {
		result, err := RunTimeout(ctx, clock, TimeoutPlan{Duration: time.Hour, Signal: term}, nil, nativeInner)
		done <- timeoutRun{result, err}
	}()
	<-clock.scheduled
	cancel()
	got := <-done
	if !errors.Is(got.err, context.Canceled) || got.result.Status != 130 || got.result.Terminal != toolcontract.ProducerInterrupted {
		t.Fatalf("result = %+v, %v; want the inner interruption, not a timeout status", got.result, got.err)
	}
}

func TestParseSignalAndDuration(t *testing.T) {
	for word, want := range map[string]string{"TERM": "TERM", "sigkill": "KILL", "SIGINT": "INT", "9": "KILL", "1": "HUP", "hup": "HUP"} {
		if sig, ok := ParseSignal(word); !ok || sig.Name != want {
			t.Errorf("ParseSignal(%q) = %v, %v; want %s", word, sig, ok, want)
		}
	}
	for _, word := range []string{"", "BOGUS", "99", "SIG"} {
		if sig, ok := ParseSignal(word); ok {
			t.Errorf("ParseSignal(%q) = %v; want refused", word, sig)
		}
	}
	for word, want := range map[string]time.Duration{"1": time.Second, "0.5": 500 * time.Millisecond, "2m": 2 * time.Minute, "1.5h": 90 * time.Minute, "1d": 24 * time.Hour, "0": 0, "3s": 3 * time.Second} {
		if got, ok := ParseDuration(word); !ok || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", word, got, ok, want)
		}
	}
	for _, word := range []string{"", "-1", "1e3", "inf", "nan", "+1", "1x", "s"} {
		if got, ok := ParseDuration(word); ok {
			t.Errorf("ParseDuration(%q) = %v; want refused", word, got)
		}
	}
}

func TestFormatTimesMatchesBash(t *testing.T) {
	real, user, sys := 61*time.Second+234567*time.Microsecond, 5*time.Millisecond+999*time.Microsecond, 0*time.Second
	if got, want := FormatTimes(real, user, sys, false), "\nreal\t1m1.234s\nuser\t0m0.005s\nsys\t0m0.000s\n"; got != want {
		t.Errorf("default report = %q, want %q", got, want)
	}
	if got, want := FormatTimes(real, user, sys, true), "real 61.23\nuser 0.00\nsys 0.00\n"; got != want {
		t.Errorf("posix report = %q, want %q", got, want)
	}
	if got, want := FormatTimes(-time.Second, 0, 0, true), "real 0.00\nuser 0.00\nsys 0.00\n"; got != want {
		t.Errorf("negative interval = %q, want %q", got, want)
	}
}
