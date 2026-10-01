package progress

import (
	"fmt"
	"testing"
	"time"
)

func TestOpenTurnForgetsThePreviousTurn(t *testing.T) {
	for _, running := range []bool{false, true} {
		root := fmt.Sprintf("%s/running=%t", t.Name(), running)
		since := time.Time{}
		if running {
			since = time.Now().Add(-time.Second)
		}
		st := &clockState{
			turn:      clockSpan{accruedMs: 5000, since: since},
			work:      clockSpan{accruedMs: 4000, since: since},
			opening:   "previous-prompt",
			settledAt: time.Now().Add(-time.Hour),
		}
		if running {
			st.active = 1
		}
		clocks.mu.Lock()
		clocks.by[root] = st
		clocks.mu.Unlock()
		t.Cleanup(func() { ForgetClock(root) })

		OpenTurn(root)
		clock := Clock(root)
		if clock.ActiveMs != 0 || clock.WorkMs != 0 || clock.Running() != running {
			t.Fatalf("opened turn = %+v, want zeroed and running=%v", clock, running)
		}
		if clock.OpeningMessageID != "" || !clock.SettledAt.IsZero() {
			t.Fatalf("opened turn kept the previous turn's identity: %+v", clock)
		}
	}
}

func TestWorkClockPausesWhileAnExecutionWaitsOnAPerson(t *testing.T) {
	root := t.Name()
	t.Cleanup(func() { ForgetClock(root) })
	start := time.Date(2026, time.September, 12, 9, 0, 0, 0, time.UTC)
	st := &clockState{}
	clocks.mu.Lock()
	clocks.by[root] = st
	clocks.mu.Unlock()

	at := start.Add
	step := func(offset time.Duration, fn func(*clockState, time.Time)) {
		clocks.mu.Lock()
		defer clocks.mu.Unlock()
		fn(st, at(offset))
	}
	step(0, func(st *clockState, now time.Time) {
		st.active = 1
		st.turn.since = now
		st.syncWork(now)
	})
	step(2*time.Second, func(st *clockState, now time.Time) {
		st.waiting++
		st.syncWork(now)
	})
	// A second execution waiting on the same decision changes nothing.
	step(3*time.Second, func(st *clockState, now time.Time) {
		st.active++
		st.waiting++
		st.syncWork(now)
	})
	step(10*time.Second, func(st *clockState, now time.Time) {
		st.waiting -= 2
		st.active--
		st.syncWork(now)
	})
	step(15*time.Second, func(st *clockState, now time.Time) {
		st.active = 0
		st.turn.pause(now)
		st.syncWork(now)
	})
	if st.turn.accruedMs != 15000 {
		t.Fatalf("active = %d ms, want 15000", st.turn.accruedMs)
	}
	if st.work.accruedMs != 7000 {
		t.Fatalf("work = %d ms, want 7000 (2s before the wait and 5s after it)", st.work.accruedMs)
	}
}
