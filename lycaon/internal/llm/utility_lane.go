package llm

import (
	"context"
	"sync"
	"time"
)

// OverlayLaneWait bounds opportunistic utility admission.
const OverlayLaneWait = 10 * time.Second

type utilityLanes struct {
	mu          sync.Mutex
	lanes       map[string]*singleLane
	overlayWait time.Duration
}

type singleLane struct {
	busy              bool
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
	release, err := l.admit(wait, providerID, singleFlight, false)
	if err != nil && ctx.Err() == nil && class == UtilityClassOverlay {
		return nil, ErrLiteBusy
	}
	return release, err
}

func (l *utilityLanes) acquireForeground(ctx context.Context, providerID string, singleFlight bool) (func(), error) {
	return l.admit(ctx, providerID, singleFlight, true)
}

func (l *utilityLanes) admit(ctx context.Context, providerID string, singleFlight, foreground bool) (func(), error) {
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
		if !lane.busy && (foreground || lane.foregroundWaiting == 0) {
			if foreground {
				lane.foregroundWaiting--
			}
			lane.busy = true
			l.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					l.mu.Lock()
					lane.busy = false
					lane.notify()
					l.mu.Unlock()
				})
			}, nil
		}
		changed := lane.changed
		l.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
		}
		l.mu.Lock()
	}
}

// notify broadcasts a state change while the admission lock is held.
func (lane *singleLane) notify() {
	close(lane.changed)
	lane.changed = make(chan struct{})
}
