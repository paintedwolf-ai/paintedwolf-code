package pagedview

import (
	"sync"
	"time"
)

type scheduledNotification struct{ timer *time.Timer }

// Notifier coalesces progress with a bounded publication rate. Urgent changes
// flush the latest state promptly; the callback reads state at publication time.
type Notifier struct {
	mu       sync.Mutex
	delivery sync.Mutex
	workers  sync.WaitGroup
	interval time.Duration
	publish  func()
	next     time.Time
	pending  *scheduledNotification
	closed   bool
}

func NewNotifier(interval time.Duration, publish func()) *Notifier {
	return &Notifier{interval: max(time.Millisecond, interval), publish: publish}
}

func (n *Notifier) Notify(urgent bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return
	}
	if n.pending != nil {
		if !urgent {
			return
		}
		if n.pending.timer.Stop() {
			n.workers.Done()
		}
	}
	delay := time.Until(n.next)
	if n.next.IsZero() {
		delay = n.interval
	}
	if urgent || delay < 0 {
		delay = 0
	}
	scheduled := &scheduledNotification{}
	n.pending = scheduled
	n.workers.Add(1)
	scheduled.timer = time.AfterFunc(delay, func() { n.flush(scheduled) })
}

func (n *Notifier) flush(scheduled *scheduledNotification) {
	defer n.workers.Done()
	n.delivery.Lock()
	defer n.delivery.Unlock()
	n.mu.Lock()
	if n.closed || n.pending != scheduled {
		n.mu.Unlock()
		return
	}
	n.pending = nil
	n.next = time.Now().Add(n.interval)
	n.mu.Unlock()
	n.publish()
}

// Close joins active callbacks after their read context is canceled.
func (n *Notifier) Close() {
	n.mu.Lock()
	n.closed = true
	if n.pending != nil && n.pending.timer.Stop() {
		n.workers.Done()
	}
	n.pending = nil
	n.mu.Unlock()
	n.workers.Wait()
}
