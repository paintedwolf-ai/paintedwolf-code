package llm

import (
	"context"
	"slices"
	"sync"
	"time"
)

// OverlayLaneWait bounds opportunistic utility admission.
const OverlayLaneWait = 10 * time.Second
const backgroundIdleWait = 30 * time.Second

type utilityLanes struct {
	mu          sync.Mutex
	lanes       map[string]*singleLane
	overlayWait time.Duration
}

type singleLane struct {
	busy              bool
	generation        uint64
	backgroundCancel  context.CancelFunc
	backgroundReady   time.Time
	foregroundWaiting int
	foregroundQueue   []*laneWaiter
	backgroundQueue   []*laneWaiter
}

type laneWaiter struct {
	foreground  bool
	preemptable bool
	queued      bool
	notified    bool
	ready       chan struct{}
}

func (w *laneWaiter) signalLocked() {
	if !w.notified {
		w.notified = true
		close(w.ready)
	}
}

func newUtilityLanes() *utilityLanes {
	return &utilityLanes{lanes: make(map[string]*singleLane)}
}

func (l *utilityLanes) overlayWaitOrDefault() time.Duration {
	if l != nil && l.overlayWait > 0 {
		return l.overlayWait
	}
	return OverlayLaneWait
}

func (l *utilityLanes) acquire(ctx context.Context, providerID string, singleFlight bool, class UtilityClass) (func(), error) {
	wait := ctx
	if class == UtilityClassOverlay {
		var cancel context.CancelFunc
		wait, cancel = context.WithTimeout(ctx, l.overlayWaitOrDefault())
		defer cancel()
	}
	release, err := l.admit(wait, providerID, singleFlight, false, nil)
	if err != nil && ctx.Err() == nil && class == UtilityClassOverlay {
		return nil, ErrLiteBusy
	}
	return release, err
}

func (l *utilityLanes) acquireForeground(ctx context.Context, providerID string, singleFlight bool) (func(), error) {
	return l.admit(ctx, providerID, singleFlight, true, nil)
}

// acquireBackground allows foreground work to cancel a dispensable projection.
func (l *utilityLanes) acquireBackground(ctx context.Context, providerID string, singleFlight bool) (context.Context, func(), error) {
	work, cancel := context.WithCancel(ctx)
	release, err := l.admit(work, providerID, singleFlight, false, cancel)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return work, func() { cancel(); release() }, nil
}

func (l *utilityLanes) admit(ctx context.Context, providerID string, singleFlight, foreground bool, preempt context.CancelFunc) (func(), error) {
	release, _, _, err := l.admitWithRevalidate(ctx, providerID, singleFlight, foreground, preempt, nil, nil)
	return release, err
}

func (l *utilityLanes) laneLocked(providerID string) *singleLane {
	lane := l.lanes[providerID]
	if lane == nil {
		lane = &singleLane{}
		l.lanes[providerID] = lane
	}
	return lane
}

func preemptBackgroundLocked(ctx context.Context, lane *singleLane, foreground bool) {
	if foreground && lane.backgroundCancel != nil && ctx.Err() == nil {
		lane.backgroundCancel()
		lane.backgroundCancel = nil
		lane.busy = false
	}
}

func wakeNextLocked(lane *singleLane) {
	if lane.busy {
		return
	}
	if len(lane.foregroundQueue) > 0 {
		lane.foregroundQueue[0].signalLocked()
		return
	}
	if len(lane.backgroundQueue) > 0 {
		lane.backgroundQueue[0].signalLocked()
	}
}

func enqueueWaiterLocked(lane *singleLane, w *laneWaiter) {
	if w.queued {
		return
	}
	w.queued = true
	w.notified = false
	w.ready = make(chan struct{})
	if w.foreground {
		lane.foregroundWaiting++
		lane.foregroundQueue = append(lane.foregroundQueue, w)
	} else {
		lane.backgroundQueue = append(lane.backgroundQueue, w)
	}
	wakeNextLocked(lane)
}

func detachWaiterLocked(lane *singleLane, w *laneWaiter) {
	if !w.queued {
		return
	}
	w.queued = false
	if w.foreground {
		lane.foregroundWaiting--
		lane.foregroundQueue = slices.DeleteFunc(lane.foregroundQueue, func(cur *laneWaiter) bool { return cur == w })
	} else {
		lane.backgroundQueue = slices.DeleteFunc(lane.backgroundQueue, func(cur *laneWaiter) bool { return cur == w })
	}
}

func removeWaiterLocked(lane *singleLane, w *laneWaiter) {
	detachWaiterLocked(lane, w)
	wakeNextLocked(lane)
}

func (l *utilityLanes) tryClaimLocked(lane *singleLane, foreground bool, preempt context.CancelFunc, waiter *laneWaiter) (func(), bool, time.Duration) {
	if lane.busy {
		return nil, false, 0
	}
	if foreground {
		if waiter == nil {
			if len(lane.foregroundQueue) > 0 {
				return nil, false, 0
			}
		} else if len(lane.foregroundQueue) == 0 || lane.foregroundQueue[0] != waiter {
			return nil, false, 0
		}
	} else {
		if lane.foregroundWaiting > 0 || len(lane.foregroundQueue) > 0 {
			return nil, false, 0
		}
		if waiter == nil {
			if len(lane.backgroundQueue) > 0 {
				return nil, false, 0
			}
		} else if len(lane.backgroundQueue) == 0 || lane.backgroundQueue[0] != waiter {
			return nil, false, 0
		}
		if preempt != nil {
			if idle := time.Until(lane.backgroundReady); idle > 0 {
				return nil, false, idle
			}
		}
	}
	if waiter != nil {
		detachWaiterLocked(lane, waiter)
	}
	lane.busy = true
	lane.generation++
	generation := lane.generation
	lane.backgroundCancel = preempt
	var once sync.Once
	release := func() {
		once.Do(func() {
			l.mu.Lock()
			if lane.generation == generation {
				lane.busy = false
				lane.backgroundCancel = nil
				if foreground {
					lane.backgroundReady = time.Now().Add(backgroundIdleWait)
				}
				wakeNextLocked(lane)
			}
			l.mu.Unlock()
		})
	}
	return release, true, 0
}

// admitWithRevalidate claims immediately when uncontended; on contention it waits
// on a fair readiness signal without holding capacity, revalidates, and re-screens
// outside capacity when freshness cannot be proved.
func (l *utilityLanes) admitWithRevalidate(
	ctx context.Context,
	providerID string,
	singleFlight, foreground bool,
	preempt context.CancelFunc,
	checkFresh func() bool,
	rescreen func() error,
) (release func(), waited, rescreened bool, err error) {
	if l == nil || !singleFlight || providerID == "" {
		return func() {}, false, false, nil
	}
	for {
		l.mu.Lock()
		if err := ctx.Err(); err != nil {
			l.mu.Unlock()
			return nil, waited, rescreened, err
		}
		lane := l.laneLocked(providerID)
		preemptBackgroundLocked(ctx, lane, foreground)
		release, claimed, idleWait := l.tryClaimLocked(lane, foreground, preempt, nil)
		if claimed {
			l.mu.Unlock()
			return release, waited, rescreened, nil
		}
		waiter := &laneWaiter{foreground: foreground, preemptable: preempt != nil}
		enqueueWaiterLocked(lane, waiter)
		ready := waiter.ready
		l.mu.Unlock()
		waited = true

		for {
			var idleCh <-chan time.Time
			var timer *time.Timer
			if idleWait > 0 {
				timer = time.NewTimer(idleWait)
				idleCh = timer.C
			}
			select {
			case <-ready:
			case <-idleCh:
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				l.mu.Lock()
				removeWaiterLocked(lane, waiter)
				l.mu.Unlock()
				return nil, waited, rescreened, ctx.Err()
			}
			if timer != nil {
				timer.Stop()
			}

			if checkFresh != nil && !checkFresh() {
				l.mu.Lock()
				removeWaiterLocked(lane, waiter)
				l.mu.Unlock()
				rescreened = true
				if rescreen != nil {
					if err := rescreen(); err != nil {
						return nil, waited, rescreened, err
					}
				}
				break
			}

			l.mu.Lock()
			if err := ctx.Err(); err != nil {
				removeWaiterLocked(lane, waiter)
				l.mu.Unlock()
				return nil, waited, rescreened, err
			}
			if waiter.notified {
				waiter.ready = make(chan struct{})
				waiter.notified = false
			}
			preemptBackgroundLocked(ctx, lane, foreground)
			release, claimed, idleWait = l.tryClaimLocked(lane, foreground, preempt, waiter)
			ready = waiter.ready
			l.mu.Unlock()
			if claimed {
				return release, waited, rescreened, nil
			}
		}
	}
}
