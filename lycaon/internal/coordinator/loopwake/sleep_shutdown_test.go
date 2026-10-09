package loopwake

import (
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
