package progress

import (
	"strings"
	"sync"
	"time"
)

// runClock tracks active time for visible user turns in each session tree.
type runClock struct {
	mu sync.Mutex
	by map[string]*clockState
}

type clockState struct {
	turn clockSpan
	work clockSpan
	// active counts executing prompts in the tree; waiting counts executions
	// blocked on a person's decision.
	active    int
	waiting   int
	opening   string
	settledAt time.Time
}

type clockSpan struct {
	accruedMs int64
	since     time.Time // zero when paused
}

func (s *clockSpan) elapsedMs(now time.Time) int64 {
	if s.since.IsZero() {
		return s.accruedMs
	}
	return s.accruedMs + now.Sub(s.since).Milliseconds()
}

func (s *clockSpan) pause(now time.Time) {
	s.accruedMs = s.elapsedMs(now)
	s.since = time.Time{}
}

func (s *clockSpan) reset(now time.Time) {
	s.accruedMs = 0
	if !s.since.IsZero() {
		s.since = now
	}
}

// syncWork runs the work span only while something executes without waiting on a person.
func (st *clockState) syncWork(now time.Time) {
	shouldRun := st.active > 0 && st.waiting == 0
	running := !st.work.since.IsZero()
	switch {
	case shouldRun && !running:
		st.work.since = now
	case !shouldRun && running:
		st.work.pause(now)
	}
}

// TurnClock is one visible user turn's clock at an instant.
type TurnClock struct {
	// OpeningMessageID is the prompt that opened the visible turn; empty when
	// the tree has run without one.
	OpeningMessageID string
	// ActiveMs is banked time while any prompt in the tree executed,
	// including approval waits inside an execution.
	ActiveMs int64
	// WorkMs is the part of ActiveMs when no execution waited on a person.
	WorkMs int64
	// RunningAt is when the clock last resumed; zero while paused.
	RunningAt time.Time
	// SettledAt is when the clock last paused; zero before its first pause.
	SettledAt time.Time
}

// Running reports whether the clock is advancing.
func (c TurnClock) Running() bool { return !c.RunningAt.IsZero() }

var clocks = &runClock{by: make(map[string]*clockState)}

func (c *runClock) get(key string) *clockState {
	st := c.by[key]
	if st == nil {
		st = &clockState{}
		c.by[key] = st
	}
	return st
}

// withClock runs fn under the clock lock for a non-empty root.
func withClock(root string, fn func(st *clockState, now time.Time)) {
	key := strings.TrimSpace(root)
	if key == "" {
		return
	}
	clocks.mu.Lock()
	defer clocks.mu.Unlock()
	fn(clocks.get(key), time.Now())
}

// OpenTurn starts a new visible user turn. The turn has no opening message
// until AnchorTurn names one.
func OpenTurn(root string) {
	withClock(root, func(st *clockState, now time.Time) {
		st.turn.reset(now)
		st.work.reset(now)
		st.opening = ""
		st.settledAt = time.Time{}
	})
}

// AnchorTurn names the prompt that opened the current visible user turn.
func AnchorTurn(root, openingMessageID string) {
	withClock(root, func(st *clockState, _ time.Time) {
		st.opening = strings.TrimSpace(openingMessageID)
	})
}

// TurnStarted reports whether the clock crossed from idle to running.
func TurnStarted(root string) (started bool) {
	withClock(root, func(st *clockState, now time.Time) {
		st.active++
		if st.active == 1 {
			st.turn.since = now
			started = true
		}
		st.syncWork(now)
	})
	return started
}

// TurnFinished reports whether the clock crossed from running to idle.
func TurnFinished(root string) (stopped bool) {
	withClock(root, func(st *clockState, now time.Time) {
		if st.active == 0 {
			return
		}
		st.active--
		if st.active == 0 {
			st.turn.pause(now)
			st.settledAt = now
			stopped = true
		}
		st.syncWork(now)
	})
	return stopped
}

// WaitStarted records an execution blocking on a person's decision.
func WaitStarted(root string) {
	withClock(root, func(st *clockState, now time.Time) {
		st.waiting++
		st.syncWork(now)
	})
}

// WaitFinished records that a blocked execution may continue.
func WaitFinished(root string) {
	withClock(root, func(st *clockState, now time.Time) {
		if st.waiting == 0 {
			return
		}
		st.waiting--
		st.syncWork(now)
	})
}

// Clock returns the visible user turn's banked and live state.
func Clock(root string) TurnClock {
	key := strings.TrimSpace(root)
	if key == "" {
		return TurnClock{}
	}
	clocks.mu.Lock()
	defer clocks.mu.Unlock()
	st := clocks.by[key]
	if st == nil {
		return TurnClock{}
	}
	return TurnClock{
		OpeningMessageID: st.opening,
		ActiveMs:         st.turn.accruedMs,
		WorkMs:           st.work.accruedMs,
		RunningAt:        st.turn.since,
		SettledAt:        st.settledAt,
	}
}

// RestoreTurnClock installs a durable clock for a root this process has not
// clocked, such as after a restart. It reports whether the clock was installed.
func RestoreTurnClock(root string, clock TurnClock) (restored bool) {
	withClock(root, func(st *clockState, _ time.Time) {
		if st.opening != "" || st.active > 0 || st.turn.accruedMs > 0 {
			return
		}
		st.opening = strings.TrimSpace(clock.OpeningMessageID)
		st.turn.accruedMs = clock.ActiveMs
		st.work.accruedMs = clock.WorkMs
		st.settledAt = clock.SettledAt
		restored = true
	})
	return restored
}

// ForgetClock removes a session tree's clock.
func ForgetClock(root string) {
	key := strings.TrimSpace(root)
	if key == "" {
		return
	}
	clocks.mu.Lock()
	defer clocks.mu.Unlock()
	delete(clocks.by, key)
}
