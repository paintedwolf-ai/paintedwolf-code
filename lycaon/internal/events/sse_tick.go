package events

import (
	"sync"
	"time"
)

var (
	heartbeatInterval   = HeartbeatInterval
	heartbeatIntervalMu sync.RWMutex
)

// HeartbeatTicker returns a ticker for SSE idle pings.
func HeartbeatTicker() *time.Ticker {
	heartbeatIntervalMu.RLock()
	interval := heartbeatInterval
	heartbeatIntervalMu.RUnlock()
	return time.NewTicker(interval)
}

// BurstEndComment closes a burst of SSE events; readers ignore it like any comment.
const BurstEndComment = ": idle\n\n"

// burstEndDelays count from a stream's last event. WebKitGTK 2.52 and 2.54 fetch streams
// hold the tail of a network read until more bytes arrive, so the burst's last event would
// wait for the heartbeat. A comment in a later read releases it; the second one covers a
// reader that took the first comment in the same read as the burst.
var burstEndDelays = [...]time.Duration{25 * time.Millisecond, time.Second}

// BurstEnd schedules the comments a stream writes once its events go quiet.
type BurstEnd struct {
	timer *time.Timer
	next  int
}

// NewBurstEnd returns a schedule that owes nothing until an event is written.
func NewBurstEnd() *BurstEnd {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	return &BurstEnd{timer: timer, next: len(burstEndDelays)}
}

// C fires when the stream owes a burst-end comment, and is nil while it owes none.
func (b *BurstEnd) C() <-chan time.Time {
	if b.next >= len(burstEndDelays) {
		return nil
	}
	return b.timer.C
}

// Wrote restarts the schedule after an event.
func (b *BurstEnd) Wrote() {
	b.next = 0
	b.timer.Reset(burstEndDelays[0])
}

// Ended records a written comment and schedules the next one.
func (b *BurstEnd) Ended() {
	b.next++
	if b.next < len(burstEndDelays) {
		b.timer.Reset(burstEndDelays[b.next] - burstEndDelays[b.next-1])
	}
}

// Stop releases the timer.
func (b *BurstEnd) Stop() {
	b.timer.Stop()
}

// SetHeartbeatIntervalForTests overrides the SSE ping interval; returns a restore func.
func SetHeartbeatIntervalForTests(d time.Duration) func() {
	heartbeatIntervalMu.Lock()
	prev := heartbeatInterval
	heartbeatInterval = d
	heartbeatIntervalMu.Unlock()
	return func() {
		heartbeatIntervalMu.Lock()
		heartbeatInterval = prev
		heartbeatIntervalMu.Unlock()
	}
}
