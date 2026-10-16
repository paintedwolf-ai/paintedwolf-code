package llm

import (
	"context"
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
	changed           chan struct{}
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

func (l *utilityLanes) admit(ctx context.Context, providerID string, singleFlight, foreground bool, preempt context.CancelFunc) (func(), error) {
	if l == nil || !singleFlight || providerID == "" {
		return func() {}, nil
	}
	l.mu.Lock()
	lane := l.lanes[providerID]
	if lane == nil {
		lane = &singleLane{changed: make(chan struct{})}
		l.lanes[providerID] = lane
	}
	if foreground {
		lane.foregroundWaiting++
		if lane.backgroundCancel != nil && ctx.Err() == nil {
			lane.backgroundCancel()
			lane.backgroundCancel = nil
			lane.busy = false
			lane.notify()
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			if foreground {
				lane.foregroundWaiting--
				lane.notify()
			}
			l.mu.Unlock()
			return nil, err
		}
		idleWait := time.Duration(0)
		if preempt != nil {
			idleWait = time.Until(lane.backgroundReady)
		}
		if !lane.busy && (foreground || lane.foregroundWaiting == 0) && idleWait <= 0 {
			if foreground {
				lane.foregroundWaiting--
			}
			lane.busy = true
			lane.generation++
			generation := lane.generation
			lane.backgroundCancel = preempt
			l.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					l.mu.Lock()
					if lane.generation == generation {
						lane.busy = false
						lane.backgroundCancel = nil
						if foreground {
							lane.backgroundReady = time.Now().Add(backgroundIdleWait)
						}
						lane.notify()
					}
					l.mu.Unlock()
				})
			}, nil
		}
		changed := lane.changed
		var idle <-chan time.Time
		var timer *time.Timer
		if !lane.busy && idleWait > 0 {
			timer = time.NewTimer(idleWait)
			idle = timer.C
		}
		l.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
		case <-idle:
		}
		if timer != nil {
			timer.Stop()
		}
		l.mu.Lock()
	}
}

// notify broadcasts a state change while the admission lock is held.
func (lane *singleLane) notify() {
	close(lane.changed)
	lane.changed = make(chan struct{})
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
