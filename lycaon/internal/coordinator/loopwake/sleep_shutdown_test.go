package loopwake

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestStopSleepTimersDisarmsArmedWaits(t *testing.T) {
	loop := NewLoopEngine()
	fired := make(chan struct{})
	st := loop.Waits.sleep.state("session-1")
	st.mu.Lock()
	st.timer = time.AfterFunc(time.Hour, func() { close(fired) })
	generation := st.timerGeneration
	st.mu.Unlock()

	loop.Waits.StopSleepTimers()

	st.mu.Lock()
	defer st.mu.Unlock()
	if st.timer != nil {
		t.Fatal("wait timer is still armed after shutdown")
	}
	if st.timerGeneration == generation {
		t.Fatal("shutdown left the timer generation unchanged, so a firing timer would still act")
	}
	var stopped *Waits
	stopped.StopSleepTimers()
}

func TestStopSleepTimersSealsRearming(t *testing.T) {
	loop := NewLoopEngine()
	waits := loop.Waits
	waits.EnterSleep(t.Context(), "session-1", time.Now().Add(time.Hour), "fixture", []WaitTrigger{WaitTriggerTimer}, nil, SleepMoverHost)
	waits.StopSleepTimers()
	waits.EnterSleep(t.Context(), "session-2", time.Now().Add(time.Hour), "fixture", []WaitTrigger{WaitTriggerTimer}, nil, SleepMoverHost)
	if _, armed := waits.sleep.Load("session-2"); armed {
		t.Fatal("shutdown admitted another wait")
	}
	if err := waits.WaitSleepTimers(t.Context()); err != nil {
		t.Fatalf("drain waits: %v", err)
	}
	waits.StopSleepTimers()
}

func TestStopSleepTimersCancelsAndDrainsFiringCallback(t *testing.T) {
	waits := NewLoopEngine().Waits
	entered, released, cancelled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var publications atomic.Int32
	waits.setDeps(WaitsDeps{PublishWaitLease: func(ctx context.Context, _ string, _ WaitLease) {
		if publications.Add(1) != 2 {
			return
		}
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-released
	}})
	waits.EnterSleep(t.Context(), "session-1", time.Now().Add(time.Millisecond), "fixture", []WaitTrigger{WaitTriggerTimer}, nil, SleepMoverHost)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timer did not deliver")
	}
	waits.StopSleepTimers()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not cancel firing delivery")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waits.WaitSleepTimers(ctx); err == nil {
		t.Fatal("shutdown finished while callback still owns the host")
	}
	close(released)
	if err := waits.WaitSleepTimers(t.Context()); err != nil {
		t.Fatalf("drain callback: %v", err)
	}
	waits.EnterSleep(t.Context(), "session-2", time.Now().Add(time.Millisecond), "fixture", []WaitTrigger{WaitTriggerTimer}, nil, SleepMoverHost)
	if publications.Load() != 2 {
		t.Fatal("stopped wait published another lease")
	}
}
